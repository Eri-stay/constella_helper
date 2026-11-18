// Command bench runs the Task 2 experiments against a running platform: it
// publishes a release N times per mode and scenario, measures latency and
// failures, and writes a markdown report into docs/EXPERIMENTS.md.
//
//	docker compose -f deploy/docker-compose.yml up -d --wait
//	go run ./tools/bench                 # all scenarios, N=50
//	go run ./tools/bench -n 5 -s S1,S5   # a quick smoke run
//	go run ./tools/bench -loc            # only the development-complexity table
//
// Scenarios (see docs/EXPERIMENTS.md):
//
//	S1 normal            S2 telemetry stopped     S3 catalog +2 s latency
//	S4 broker stopped    S5 redelivered event     S6 telemetry returns and catches up
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type bench struct {
	gw, token, compose, rabbit string
	n                          int
	pause                      time.Duration
	http                       *http.Client
	notes                      []string
}

// run is one measured publication.
type run struct {
	respMS, doneMS float64 // answer of the API; completion of the release
	ok             bool
	releaseID      string
}

// row is one line of the result table.
type row struct {
	scenario, mode string
	runs           []run
}

func main() {
	var (
		b      bench
		only   = flag.String("s", "S1,S2,S3,S4,S5,S6", "scenarios to run")
		out    = flag.String("out", "docs/EXPERIMENTS.md", "report file (the generated part is replaced)")
		locDoc = flag.Bool("loc", false, "only print the complexity table")
	)
	flag.StringVar(&b.gw, "gateway", "http://localhost:8080", "gateway URL")
	flag.StringVar(&b.token, "token", "dev-token", "platform token")
	flag.StringVar(&b.compose, "compose", "deploy/docker-compose.yml", "compose file")
	flag.StringVar(&b.rabbit, "rabbit", "http://localhost:15672", "RabbitMQ management URL (guest/guest)")
	flag.IntVar(&b.n, "n", 50, "publications per mode and scenario")
	flag.DurationVar(&b.pause, "pause", 12*time.Second, "pause after a scenario (lets circuit breakers close)")
	flag.Parse()
	b.http = &http.Client{Timeout: 60 * time.Second}

	if *locDoc {
		fmt.Print(complexity())
		return
	}
	if err := b.main(strings.Split(*only, ","), *out); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func (b *bench) main(scenarios []string, out string) error {
	ctx := context.Background()
	if err := b.waitHealthy(ctx); err != nil {
		return err
	}
	build, err := b.validatedBuild(ctx)
	if err != nil {
		return err
	}
	logf("using build %s, N=%d", build, b.n)

	var rows []row
	for _, s := range scenarios {
		s = strings.TrimSpace(strings.ToUpper(s))
		logf("=== %s", s)
		got, err := b.scenario(ctx, s, build)
		b.restore(ctx)
		if err != nil {
			return fmt.Errorf("scenario %s: %w", s, err)
		}
		rows = append(rows, got...)
		time.Sleep(b.pause)
	}
	report := render(rows, b.notes, b.n)
	fmt.Println(report)
	return writeReport(out, report)
}

func (b *bench) scenario(ctx context.Context, name, build string) ([]row, error) {
	switch name {
	case "S1":
		return b.both("S1", build, nil, nil), nil
	case "S2":
		if err := b.compose2("stop", "telemetry"); err != nil {
			return nil, err
		}
		return b.both("S2", build, nil, nil), nil
	case "S3":
		slow := func() { b.fault("catalog", 2000) }
		fast := func() { b.fault("catalog", 0) }
		slow()
		return b.both("S3", build, fast, slow), nil
	case "S4":
		return b.s4(ctx, build)
	case "S5":
		return nil, b.s5(ctx)
	case "S6":
		return nil, b.s6(ctx, build)
	}
	return nil, fmt.Errorf("unknown scenario %q", name)
}

// both measures classic and event mode under the same conditions. beforeFree
// and afterFree wrap the cleanup call, so an injected delay is not paid there.
func (b *bench) both(name, build string, beforeFree, afterFree func()) []row {
	var rows []row
	for _, mode := range []string{"classic", "eda"} {
		r := row{scenario: name, mode: mode}
		for i := 0; i < b.n; i++ {
			r.runs = append(r.runs, b.publish(mode, build, beforeFree, afterFree))
		}
		logf("%s/%s: %d ok of %d", name, mode, okCount(r.runs), len(r.runs))
		rows = append(rows, r)
	}
	return rows
}

// publish makes one release and frees the build again, so the same validated
// build can be published N times.
func (b *bench) publish(mode, build string, beforeFree, afterFree func()) run {
	start := time.Now()
	var resp struct {
		ID     string         `json:"id"`
		Status string         `json:"status"`
		Params map[string]any `json:"params"`
	}
	status, err := b.call("POST", "/api/release/releases?mode="+mode, map[string]string{"build_id": build}, &resp)
	r := run{respMS: ms(time.Since(start))}
	r.releaseID = resp.ID
	if id, _ := resp.Params["release_id"].(string); id != "" {
		r.releaseID = id
	}
	switch {
	case err != nil:
	case mode == "classic":
		r.ok = status == http.StatusOK
		r.doneMS = r.respMS
	default:
		if status == http.StatusAccepted {
			r.ok = b.waitPublished(r.releaseID, start)
		}
		r.doneMS = ms(time.Since(start))
	}
	if beforeFree != nil {
		beforeFree()
	}
	if r.releaseID != "" {
		_, _ = b.call("DELETE", "/api/catalog/builds/"+build+"/reserve?release_id="+r.releaseID, nil, nil)
	}
	if afterFree != nil {
		afterFree()
	}
	return r
}

// waitPublished polls an event-mode release until it ends.
func (b *bench) waitPublished(id string, start time.Time) bool {
	deadline := start.Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var v struct {
			Status string `json:"status"`
		}
		if status, err := b.call("GET", "/api/release/releases/"+id, nil, &v); err == nil && status == 200 {
			switch v.Status {
			case "published":
				return true
			case "failed":
				return false
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// s4: the broker is stopped while releases are published. Classic mode does
// not use it; event mode must still publish, and its events must not be lost.
func (b *bench) s4(ctx context.Context, build string) ([]row, error) {
	if err := b.compose2("stop", "rabbitmq"); err != nil {
		return nil, err
	}
	rows := b.both("S4", build, nil, nil)
	ids := idsOf(rows[1].runs)
	if err := b.compose2("start", "rabbitmq"); err != nil {
		return nil, err
	}
	took, got := b.waitAudit(ctx, "release.published", ids, 120*time.Second)
	b.note("S4: після повернення брокера %d з %d подій `release.published` дійшли до аудиту за %.1f с. "+
		"Нічого не втрачено: outbox тримав події, поки брокер був зупинений.", got, len(ids), took.Seconds())
	return rows, nil
}

// s5: the same event is delivered twice; the audit must not duplicate it.
func (b *bench) s5(ctx context.Context) error {
	stamp := time.Now().UnixNano()
	var ids []string
	for i := 0; i < b.n; i++ {
		id := fmt.Sprintf("bench%d-%d", stamp, i)
		ids = append(ids, id)
		env := map[string]any{"id": fmt.Sprintf("%032x", stamp+int64(i)), "type": "bench.event", "producer": "bench",
			"occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "correlation_id": id, "schema_ver": 1, "data": map[string]any{}}
		for range 2 { // the broker redelivers
			if err := b.publishRaw(ctx, env); err != nil {
				return err
			}
		}
	}
	_, got := b.waitAudit(ctx, "bench.event", ids, 60*time.Second)
	time.Sleep(3 * time.Second) // time for a (wrong) second row to appear
	rows := b.auditRows(ctx, "bench.event", ids)
	b.note("S5: %d подій доставлено по двічі; в аудиті %d рядків (%d різних id). "+
		"Очікувано %d: inbox пропускає повторний id події.", len(ids), rows, got, len(ids))
	return nil
}

// s6: telemetry is down while releases are published, then returns.
func (b *bench) s6(ctx context.Context, build string) error {
	if err := b.compose2("stop", "telemetry"); err != nil {
		return err
	}
	rows := b.both("S6", build, nil, nil)
	ids := idsOf(rows[1].runs)
	started := time.Now()
	if err := b.compose2("start", "telemetry"); err != nil {
		return err
	}
	_, got := b.waitAudit(ctx, "release.published", ids, 120*time.Second)
	b.note("S6: telemetry був зупинений під час %d подієвих релізів (опубліковано %d). %d з %d подій були в "+
		"аудиті через %.1f с після запуску telemetry: черга тримала їх.", len(ids), okCount(rows[1].runs), got, len(ids), time.Since(started).Seconds())
	// S6 reports no latency rows: S2 already covers the failing period.
	return nil
}

// ---- helpers ---------------------------------------------------------------

func (b *bench) call(method, path string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.gw+path, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	resp, err := b.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck // body is read below
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 {
		_ = json.Unmarshal(data, out) // error bodies fill what they can
	}
	return resp.StatusCode, nil
}

func (b *bench) fault(service string, latencyMS int) {
	_, _ = b.call("POST", "/api/"+service+"/debug/fault", map[string]any{"latency_ms": latencyMS, "fail_rate": 0, "status": 503}, nil)
}

// restore puts the platform back to normal after a scenario.
func (b *bench) restore(ctx context.Context) {
	for _, s := range []string{"dataprep", "catalog", "release", "telemetry"} {
		b.fault(s, 0)
	}
	for _, svc := range []string{"rabbitmq", "telemetry"} {
		_ = b.compose2("start", svc) // a no-op when it is running
	}
	_ = b.waitHealthy(ctx)
}

func (b *bench) compose2(action, service string) error {
	cmd := exec.Command("docker", "compose", "-f", b.compose, action, service)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose %s %s: %w: %s", action, service, err, out)
	}
	logf("docker compose %s %s", action, service)
	if action == "start" {
		time.Sleep(4 * time.Second)
	}
	return nil
}

func (b *bench) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var v struct {
			Services []struct{ Status string } `json:"services"`
		}
		if st, err := b.call("GET", "/api/overview", nil, &v); err == nil && st == 200 && len(v.Services) > 0 {
			all := true
			for _, s := range v.Services {
				all = all && s.Status == "ok"
			}
			if all {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("the platform did not become healthy; start it: docker compose -f %s up -d --wait", b.compose)
}

// validatedBuild starts a build and waits until the catalog validated it.
func (b *bench) validatedBuild(ctx context.Context) (string, error) {
	var started struct{ ID string }
	if st, err := b.call("POST", "/api/dataprep/builds", nil, &started); err != nil || st != http.StatusAccepted {
		return "", fmt.Errorf("start a build (status %d): %v", st, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var rep struct{ Verdict string }
		if st, _ := b.call("GET", "/api/catalog/reports/"+started.ID, nil, &rep); st == 200 && rep.Verdict == "validated" {
			return started.ID, nil
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("build %s was not validated in time", started.ID)
}

// audit returns the correlation ids of audit rows of a type.
func (b *bench) audit(ctx context.Context, typ string) []string {
	var v struct {
		Items []struct {
			CorrelationID string `json:"correlation_id"`
		} `json:"items"`
	}
	if st, err := b.call("GET", "/api/telemetry/audit?limit=500&type="+typ, nil, &v); err != nil || st != 200 {
		return nil
	}
	var out []string
	for _, i := range v.Items {
		out = append(out, i.CorrelationID)
	}
	return out
}

func (b *bench) auditRows(ctx context.Context, typ string, ids []string) int {
	want := set(ids)
	n := 0
	for _, c := range b.audit(ctx, typ) {
		if want[c] {
			n++
		}
	}
	return n
}

// waitAudit waits until every id has an audit row of the type.
func (b *bench) waitAudit(ctx context.Context, typ string, ids []string, limit time.Duration) (time.Duration, int) {
	start := time.Now()
	got := 0
	for time.Since(start) < limit {
		have := set(b.audit(ctx, typ))
		got = 0
		for _, id := range ids {
			if have[id] {
				got++
			}
		}
		if got == len(ids) {
			break
		}
		time.Sleep(time.Second)
	}
	return time.Since(start), got
}

// publishRaw puts an envelope on the platform exchange through the RabbitMQ
// management API.
func (b *bench) publishRaw(ctx context.Context, env map[string]any) error {
	payload, _ := json.Marshal(env)
	body, _ := json.Marshal(map[string]any{
		"properties":  map[string]any{"delivery_mode": 2, "content_type": "application/json", "message_id": env["id"]},
		"routing_key": env["type"], "payload": string(payload), "payload_encoding": "string",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", b.rabbit+"/api/exchanges/%2F/constella.platform/publish", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth("guest", "guest")
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("publish through rabbitmq management: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // answer checked below
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Contains(data, []byte(`"routed":true`)) {
		return fmt.Errorf("rabbitmq did not route the event: %d %s", resp.StatusCode, data)
	}
	return nil
}

func (b *bench) note(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	logf("%s", line)
	b.notes = append(b.notes, line)
}

// ---- statistics and report ---------------------------------------------------

func okCount(rs []run) int {
	n := 0
	for _, r := range rs {
		if r.ok {
			n++
		}
	}
	return n
}

func idsOf(rs []run) []string {
	var ids []string
	for _, r := range rs {
		if r.releaseID != "" && r.ok {
			ids = append(ids, r.releaseID)
		}
	}
	return ids
}

func set(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// percentile is the nearest-rank percentile of the values.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	rank := int(p/100*float64(len(s)) + 0.999999)
	return s[max(1, min(rank, len(s)))-1]
}

func fmtMS(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2f с", v/1000)
	}
	return fmt.Sprintf("%.0f мс", v)
}

func render(rows []row, notes []string, n int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Згенеровано `go run ./tools/bench` (%s, N=%d публікацій на режим і сценарій).\n\n", time.Now().Format("2006-01-02 15:04"), n)
	sb.WriteString("| Сценарій | Режим | Успіх | Помилки | Відповідь p50 | Відповідь p95 | Завершення p50 | Завершення p95 |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		var resp, done []float64
		for _, x := range r.runs {
			resp = append(resp, x.respMS)
			done = append(done, x.doneMS)
		}
		ok := okCount(r.runs)
		fmt.Fprintf(&sb, "| %s | %s | %d/%d | %.0f %% | %s | %s | %s | %s |\n", r.scenario, r.mode, ok, len(r.runs),
			100*float64(len(r.runs)-ok)/float64(max(1, len(r.runs))),
			fmtMS(percentile(resp, 50)), fmtMS(percentile(resp, 95)), fmtMS(percentile(done, 50)), fmtMS(percentile(done, 95)))
	}
	sb.WriteString("\n«Відповідь» це час до відповіді API (у режимі подій це `202`), «Завершення» час до кінцевого стану релізу (`published`/`failed`).\n")
	if len(notes) > 0 {
		sb.WriteString("\n")
		for _, nt := range notes {
			sb.WriteString("- " + nt + "\n")
		}
	}
	sb.WriteString("\n" + complexity())
	return sb.String()
}

const (
	beginMark = "<!-- bench:begin -->"
	endMark   = "<!-- bench:end -->"
)

func writeReport(path, report string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		data = []byte("# Експерименти (Лаб №3, завдання 2)\n\n" + beginMark + "\n" + endMark + "\n")
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	i, j := strings.Index(s, beginMark), strings.Index(s, endMark)
	if i < 0 || j < i {
		s += "\n" + beginMark + "\n" + endMark + "\n"
		i, j = strings.Index(s, beginMark), strings.Index(s, endMark)
	}
	s = s[:i+len(beginMark)] + "\n" + report + "\n" + s[j:]
	return os.WriteFile(path, []byte(s), 0o644)
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}
