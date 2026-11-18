package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"constella/services/catalog/internal/app"
)

func TestReservationProtocol(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, file := sampleCosmos(t)
	svc, repo, _ := newCatalogService(t, fixedInspector{snap: goodSnapshot()}, dir)
	if _, err := repo.Reserve(ctx, "b1", "r1"); !errors.Is(err, app.ErrNotValidated) {
		t.Fatalf("reserve before validation: %v", err)
	}
	if err := svc.HandleBuildCompleted(ctx, completedEvent(t, file)); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Reserve(ctx, "b1", "r1")
	if err != nil || got.Artifact != file || got.SHA256 != "a" {
		t.Fatalf("reserve: %+v (err %v)", got, err)
	}
	if _, err := repo.Reserve(ctx, "b1", "r1"); err != nil {
		t.Errorf("the same release may reserve again: %v", err)
	}
	if _, err := repo.Reserve(ctx, "b1", "r2"); !errors.Is(err, app.ErrReserved) {
		t.Errorf("another release: %v, want ErrReserved", err)
	}
	if err := repo.MarkReleased(ctx, "b1", "r2"); !errors.Is(err, app.ErrNotReserved) {
		t.Errorf("mark by a stranger: %v", err)
	}
	if err := repo.MarkReleased(ctx, "b1", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Reserve(ctx, "b1", "r2"); !errors.Is(err, app.ErrReleased) {
		t.Errorf("after release: %v, want ErrReleased", err)
	}

	// Compensation frees the build, also after it was marked released.
	if err := repo.Cancel(ctx, "b1", "r1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Cancel(ctx, "b1", "r1"); err != nil {
		t.Errorf("cancel is idempotent: %v", err)
	}
	if _, err := repo.Reserve(ctx, "b1", "r2"); err != nil {
		t.Errorf("reserve after cancel: %v", err)
	}
}

func TestReservationHTTP(t *testing.T) {
	t.Parallel()
	dir, file := sampleCosmos(t)
	svc, _, ix := newCatalogService(t, fixedInspector{snap: goodSnapshot()}, dir)
	if err := svc.HandleBuildCompleted(context.Background(), completedEvent(t, file)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Routes(mux, svc, ix)

	post := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := post("POST", "/v1/builds/b1/reserve", `{"release_id":"r1"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sha256":"a"`) {
		t.Errorf("reserve: %d %s", rec.Code, rec.Body)
	}
	if rec := post("POST", "/v1/builds/b1/reserve", `{"release_id":"r2"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "BUILD_RESERVED") {
		t.Errorf("conflict: %d %s", rec.Code, rec.Body)
	}
	if rec := post("POST", "/v1/builds/nope/reserve", `{"release_id":"r1"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "BUILD_NOT_VALIDATED") {
		t.Errorf("unknown build: %d %s", rec.Code, rec.Body)
	}
	if rec := post("POST", "/v1/builds/b1/release", `{}`); rec.Code != 400 {
		t.Errorf("missing release id: %d", rec.Code)
	}
	if rec := post("POST", "/v1/builds/b1/release", `{"release_id":"r1"}`); rec.Code != 200 {
		t.Errorf("mark released: %d %s", rec.Code, rec.Body)
	}
	if rec := post("DELETE", "/v1/builds/b1/reserve?release_id=r1", ""); rec.Code != 200 {
		t.Errorf("cancel: %d %s", rec.Code, rec.Body)
	}
}
