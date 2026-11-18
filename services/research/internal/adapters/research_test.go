package adapters

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"constella/internal/domain/compare"
	"constella/internal/domain/genconst"
	"constella/internal/platform/events"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/research/internal/app"
)

func TestStoreSweepLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlitex.Open(ctx, ":memory:", Schema...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := Store{DB: db}

	sw := app.Sweep{ID: "s1", Status: app.StatusRunning, CreatedAt: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
		BuildID: "b1", Culture: "modern_iau", Total: 2, Spec: app.Spec{Culture: "modern_iau", Seeds: []uint64{1, 2}}}
	if err := st.CreateSweep(ctx, sw); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		run := app.Run{Seq: i, Params: genconst.Params{Strategy: genconst.Base, Seed: uint64(i)}, Edges: 10 + i,
			Metrics: compare.Metrics{Jaccard: 0.25}, Hash: "h"}
		if err := st.AddRun(ctx, "s1", run); err != nil {
			t.Fatal(err)
		}
	}
	end := sw.CreatedAt.Add(time.Second)
	sw.Status, sw.FinishedAt = app.StatusCompleted, &end
	ev, _ := events.New(events.ResearchRunCompleted, "research", events.ResearchCompletedData{SweepID: "s1"})
	if err := st.FinishSweep(ctx, sw, ev); err != nil {
		t.Fatal(err)
	}

	got, runs, err := st.Get(ctx, "s1")
	if err != nil || got.Status != app.StatusCompleted || got.Done != 2 || len(runs) != 2 ||
		runs[1].Edges != 11 || got.Spec.Seeds[1] != 2 || got.FinishedAt == nil {
		t.Fatalf("stored: %+v %+v (err %v)", got, runs, err)
	}
	if _, _, err := st.Get(ctx, "nope"); err != app.ErrNotFound {
		t.Errorf("unknown sweep: %v", err)
	}
	if pending, _, _ := outbox.Stats(ctx, db); pending != 1 {
		t.Errorf("outbox pending = %d, want the completion event", pending)
	}
	if running, _ := st.Running(ctx); len(running) != 0 {
		t.Errorf("a finished sweep is still running: %v", running)
	}
}

func TestWriteCSV(t *testing.T) {
	t.Parallel()
	sw := app.Sweep{ID: "s1", BuildID: "b1", Culture: "maya"}
	runs := []app.Run{{Params: genconst.Params{Strategy: genconst.Cycles, EpsDeg: 8.5, Seed: 7, MagMax: 5}, Stars: 1616, Edges: 900,
		Clusters: 120, Metrics: compare.Metrics{Jaccard: 0.123456, Precision: 0.5, Recall: 0.25, Common: 5, Reference: 20}, Hash: "abc", DurationMS: 12.5}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sw, runs); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil || len(rows) != 2 || len(rows[0]) != len(rows[1]) {
		t.Fatalf("csv: %v (err %v)", rows, err)
	}
	row := map[string]string{}
	for i, h := range rows[0] {
		row[h] = rows[1][i]
	}
	if row["strategy"] != "cycles" || row["eps_deg"] != "8.5" || row["seed"] != "7" || row["jaccard"] != "0.1235" ||
		row["reference_edges"] != "20" || row["culture"] != "maya" {
		t.Errorf("row: %v", row)
	}
}

type fakeSweeps struct{ spec app.Spec }

func (f *fakeSweeps) Start(_ context.Context, s app.Spec) (app.Sweep, error) {
	f.spec = s
	if s.Culture == "" {
		return app.Sweep{}, app.ErrInvalid
	}
	return app.Sweep{ID: "s9", Status: app.StatusRunning}, nil
}
func (f *fakeSweeps) Get(context.Context, string) (app.Sweep, []app.Run, error) {
	return app.Sweep{}, nil, app.ErrNotFound
}
func (f *fakeSweeps) List(context.Context, int) ([]app.Sweep, error) { return nil, nil }
func (f *fakeSweeps) Verify(context.Context, app.Spec) (app.Verification, error) {
	return app.Verification{Equal: true}, nil
}

func TestHTTP(t *testing.T) {
	t.Parallel()
	fake := &fakeSweeps{}
	mux := http.NewServeMux()
	Routes(mux, fake)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do("POST", "/v1/sweeps", `{"culture":"maya","strategies":["base"],"eps_list":[8,10],"params":{"gamma":4}}`); rec.Code != 202 ||
		fake.spec.Params.Gamma != 4 || len(fake.spec.EpsList) != 2 || rec.Header().Get("Location") != "/v1/sweeps/s9" {
		t.Errorf("start: %d %s spec %+v", rec.Code, rec.Body, fake.spec)
	}
	if rec := do("POST", "/v1/sweeps", `{}`); rec.Code != 400 {
		t.Errorf("invalid spec: %d", rec.Code)
	}
	if rec := do("POST", "/v1/sweeps", `{"culture":"x","unknown":1}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	if rec := do("GET", "/v1/sweeps/zzz", ""); rec.Code != 404 {
		t.Errorf("unknown sweep: %d", rec.Code)
	}
	if rec := do("GET", "/v1/strategies", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "star_paths") {
		t.Errorf("strategies: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/v1/verify", `{"culture":"maya"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"equal":true`) {
		t.Errorf("verify: %d %s", rec.Code, rec.Body)
	}
}
