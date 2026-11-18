package adapters

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"constella/internal/platform/events"
	"constella/internal/platform/sqlitex"
	"constella/services/telemetry/internal/app"
)

type recSink struct{ got []app.ClientLog }

func (*recSink) Name() string { return "rec" }
func (r *recSink) Capture(_ context.Context, l app.ClientLog) error {
	r.got = append(r.got, l)
	return nil
}

func setup(t *testing.T) (*http.ServeMux, *recSink, *app.Service) {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Schema...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := &recSink{}
	svc := app.New(Store{DB: db}, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	Routes(mux, svc)
	return mux, sink, svc
}

func do(mux http.Handler, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAuditIdempotencyKey(t *testing.T) {
	t.Parallel()
	mux, _, _ := setup(t)
	body := `{"source":"release","type":"release.published","correlation_id":"r1","data":{"build":"b1"}}`
	first := do(mux, "POST", "/v1/audit", body, "Idempotency-Key", "r1-audit")
	second := do(mux, "POST", "/v1/audit", body, "Idempotency-Key", "r1-audit")
	if first.Code != http.StatusCreated || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Errorf("first %d %s, second %d %s", first.Code, first.Body, second.Code, second.Body)
	}
	list := do(mux, "GET", "/v1/audit?type=release.", "")
	var out struct{ Items []app.AuditEntry }
	if err := json.Unmarshal(list.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.Items[0].Via != app.ViaSync {
		t.Errorf("audit list: %s (err %v)", list.Body, err)
	}
	if bad := do(mux, "POST", "/v1/audit", `{"source":""}`, ""); bad.Code != http.StatusBadRequest {
		t.Errorf("missing fields: %d", bad.Code)
	}
}

func TestEventAuditIsIdempotent(t *testing.T) {
	t.Parallel()
	_, _, svc := setup(t)
	env, _ := events.New(events.BuildValidated, "catalog", map[string]string{"build_id": "b1"})
	for range 2 { // the broker redelivers
		if err := svc.HandleEvent(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := svc.Audit(context.Background(), 10, "")
	if len(items) != 1 || items[0].Via != app.ViaEvent || items[0].Type != events.BuildValidated {
		t.Errorf("audit: %+v", items)
	}
}

func TestClientLogScrubsAndForwardsErrors(t *testing.T) {
	t.Parallel()
	mux, sink, _ := setup(t)
	rec := do(mux, "POST", "/v1/client-log",
		`{"level":"error","message":"cannot open C:\\Users\\Maria\\Documents\\x.json","stack":"at C:\\Users\\Maria\\app"}`)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "Maria") {
		t.Fatalf("response leaks the user name: %d %s", rec.Code, rec.Body)
	}
	if len(sink.got) != 1 || strings.Contains(sink.got[0].Message, "Maria") || !strings.Contains(sink.got[0].Message, "<user>") {
		t.Errorf("sink got %+v", sink.got)
	}
	if info := do(mux, "POST", "/v1/client-log", `{"level":"info","message":"started"}`); info.Code != http.StatusCreated || len(sink.got) != 1 {
		t.Errorf("info must be stored, not forwarded: %d, sink %d", info.Code, len(sink.got))
	}
	if bad := do(mux, "POST", "/v1/client-log", `{"level":"loud","message":"x"}`); bad.Code != http.StatusBadRequest {
		t.Errorf("bad level: %d", bad.Code)
	}
	stored := do(mux, "GET", "/v1/client-log", "")
	if strings.Contains(stored.Body.String(), "Maria") {
		t.Errorf("stored log leaks the user name: %s", stored.Body)
	}
}

func TestSentrySink(t *testing.T) {
	t.Parallel()
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("X-Sentry-Auth")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	sink, err := NewSentrySink(strings.Replace(srv.URL, "http://", "http://pubkey@", 1) + "/42")
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Capture(context.Background(), app.ClientLog{Level: "fatal", Message: "boom"}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/42/store/" || !strings.Contains(auth, "sentry_key=pubkey") {
		t.Errorf("sentry request: %s %s", path, auth)
	}
	if _, err := NewSentrySink("not a dsn"); err == nil {
		t.Error("bad dsn accepted")
	}
}
