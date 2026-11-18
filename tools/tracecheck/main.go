// Command tracecheck reads the newest trace of a scenario from Jaeger, prints
// it as a tree and checks that the spans the lab report needs are there.
//
//	go run ./tools/tracecheck -scenario classic
//	go run ./tools/tracecheck -scenario eda
//	go run ./tools/tracecheck -scenario classic-fail   # after: docker compose stop telemetry
//
// Exit code 1 means a span, a service or an error mark is missing.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// scenario describes what a good trace of one situation contains.
type scenario struct {
	service, operation string
	spans              []string // operation names that must exist
	services           []string // services that must appear
	errors             []string // operation names that must carry an error mark
}

var scenarios = map[string]scenario{
	"classic": {
		service: "release", operation: "release publish",
		spans:    []string{"POST /api/release/releases", "saga reserve", "saga switch", "saga mark_released", "saga audit"},
		services: []string{"gateway", "release", "catalog", "telemetry"},
	},
	"eda": {
		service: "release", operation: "release saga",
		spans: []string{"POST /api/release/releases", "release request", "release saga", "saga reserve", "saga switch",
			"saga mark_released", "publish release.published", "consume release.published"},
		services: []string{"gateway", "release", "catalog", "telemetry"},
	},
	"classic-fail": {
		service: "release", operation: "release publish",
		spans:    []string{"saga audit", "saga compensate", "compensate restore manifest", "compensate cancel reservation"},
		services: []string{"gateway", "release", "catalog"},
		errors:   []string{"saga audit", "release publish"},
	},
}

type span struct {
	SpanID        string `json:"spanID"`
	OperationName string `json:"operationName"`
	ProcessID     string `json:"processID"`
	StartTime     int64  `json:"startTime"`
	Duration      int64  `json:"duration"`
	References    []struct {
		RefType string `json:"refType"`
		SpanID  string `json:"spanID"`
	} `json:"references"`
	Tags []struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	} `json:"tags"`
}

type trace struct {
	TraceID   string `json:"traceID"`
	Spans     []span `json:"spans"`
	Processes map[string]struct {
		ServiceName string `json:"serviceName"`
	} `json:"processes"`
}

func main() {
	name := flag.String("scenario", "classic", "classic, eda or classic-fail")
	jaeger := flag.String("jaeger", "http://localhost:16686", "Jaeger base URL")
	flag.Parse()
	sc, ok := scenarios[*name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown scenario %q\n", *name)
		os.Exit(2)
	}
	tr, err := newest(*jaeger, sc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracecheck:", err)
		os.Exit(2)
	}
	print(tr)
	problems := check(tr, sc)
	fmt.Printf("\ntrace %s/trace/%s\n", *jaeger, tr.TraceID)
	if len(problems) == 0 {
		fmt.Println("OK: all expected spans are present")
		return
	}
	for _, p := range problems {
		fmt.Println("MISSING:", p)
	}
	os.Exit(1)
}

func newest(base string, sc scenario) (trace, error) {
	q := url.Values{"service": {sc.service}, "operation": {sc.operation}, "limit": {"1"}}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(base + "/api/traces?" + q.Encode())
	if err != nil {
		return trace{}, fmt.Errorf("ask jaeger: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body is read below
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return trace{}, fmt.Errorf("read jaeger answer: %w", err)
	}
	var out struct {
		Data []trace `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return trace{}, fmt.Errorf("decode jaeger answer: %w", err)
	}
	if len(out.Data) == 0 {
		return trace{}, fmt.Errorf("no trace of %q in service %q yet: run the scenario first", sc.operation, sc.service)
	}
	return out.Data[0], nil
}

func isError(s span) bool {
	for _, t := range s.Tags {
		if t.Key == "error" && t.Value == true {
			return true
		}
		if t.Key == "otel.status_code" && t.Value == "ERROR" {
			return true
		}
	}
	return false
}

func print(tr trace) {
	children := map[string][]span{}
	byID := map[string]bool{}
	for _, s := range tr.Spans {
		byID[s.SpanID] = true
	}
	var roots []span
	for _, s := range tr.Spans {
		parent := ""
		for _, r := range s.References {
			if r.RefType == "CHILD_OF" {
				parent = r.SpanID
			}
		}
		if parent == "" || !byID[parent] {
			roots = append(roots, s)
			continue
		}
		children[parent] = append(children[parent], s)
	}
	var walk func(s span, depth int)
	walk = func(s span, depth int) {
		mark := ""
		if isError(s) {
			mark = "  [ERROR]"
		}
		fmt.Printf("%s%-9s %s (%.1f ms)%s\n", strings.Repeat("  ", depth), tr.Processes[s.ProcessID].ServiceName,
			s.OperationName, float64(s.Duration)/1000, mark)
		kids := children[s.SpanID]
		sort.Slice(kids, func(i, j int) bool { return kids[i].StartTime < kids[j].StartTime })
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].StartTime < roots[j].StartTime })
	for _, r := range roots {
		walk(r, 0)
	}
}

func check(tr trace, sc scenario) []string {
	have := map[string]span{}
	services := map[string]bool{}
	for _, s := range tr.Spans {
		have[s.OperationName] = s
		services[tr.Processes[s.ProcessID].ServiceName] = true
	}
	var problems []string
	for _, n := range sc.spans {
		if _, ok := have[n]; !ok {
			problems = append(problems, "span "+n)
		}
	}
	for _, n := range sc.services {
		if !services[n] {
			problems = append(problems, "service "+n)
		}
	}
	for _, n := range sc.errors {
		if s, ok := have[n]; ok && !isError(s) {
			problems = append(problems, "error mark on "+n)
		}
	}
	return problems
}
