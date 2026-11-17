// Package archtest enforces the dependency direction of the module:
//
//	services/* ─► internal/platform/* ─► (stdlib, third party)
//	services/* ─► internal/domain/*   (pure: stdlib only + other domain packages)
//
// The platform never imports domain or services; domain never imports
// platform or services; a service never imports another service.
package archtest

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
)

const module = "constella"

type pkg struct {
	ImportPath string
	Imports    []string
}

func listPackages(t *testing.T) []pkg {
	t.Helper()
	out, err := exec.Command("go", "list", "-json", module+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []pkg
	for {
		var p pkg
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return pkgs
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
}

// serviceOf returns "services/<name>" for a path inside a service, or "".
func serviceOf(path string) string {
	rest, ok := strings.CutPrefix(path, module+"/services/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return "services/" + name
}

func TestDependencyDirection(t *testing.T) {
	t.Parallel()
	for _, p := range listPackages(t) {
		for _, imp := range p.Imports {
			if !strings.HasPrefix(imp, module+"/") {
				continue
			}
			if msg := violation(p.ImportPath, imp); msg != "" {
				t.Errorf("%s imports %s: %s", p.ImportPath, imp, msg)
			}
		}
	}
}

func violation(from, imp string) string {
	in := func(path, area string) bool { return strings.HasPrefix(path, module+"/"+area) }
	switch {
	case in(from, "internal/domain/") && !in(imp, "internal/domain/"):
		return "domain must stay pure (only other domain packages)"
	case in(from, "internal/platform/") && (in(imp, "internal/domain/") || in(imp, "services/")):
		return "platform must not depend on domain or services"
	case serviceOf(from) != "" && serviceOf(imp) != "" && serviceOf(from) != serviceOf(imp):
		return "a service must not import another service (talk over HTTP, gRPC or events)"
	}
	return ""
}

func TestViolationRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, imp string
		bad       bool
	}{
		{"constella/internal/domain/release", "constella/internal/domain/catalog", false},
		{"constella/internal/domain/release", "constella/internal/platform/outbox", true},
		{"constella/internal/platform/mq", "constella/internal/platform/events", false},
		{"constella/internal/platform/mq", "constella/internal/domain/release", true},
		{"constella/services/release/internal/app", "constella/internal/domain/release", false},
		{"constella/services/release/internal/app", "constella/services/release/internal/adapters", false},
		{"constella/services/release/internal/app", "constella/services/catalog/internal/app", true},
	}
	for _, tc := range tests {
		if got := violation(tc.from, tc.imp) != ""; got != tc.bad {
			t.Errorf("violation(%s -> %s) = %v, want %v", tc.from, tc.imp, got, tc.bad)
		}
	}
}
