package telemetry

import "testing"

func TestScrub(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{`open C:\Users\Maria\Documents\Constella\Tests\v1.json failed`, `open C:\Users\<user>\Documents\Constella\Tests\v1.json failed`},
		{`c:/users/eri/AppData/Local/x`, `c:/users/<user>/AppData/Local/x`},
		{`/home/anna/.cache/app`, `/home/<user>/.cache/app`},
		{`/Users/bob/Library`, `/Users/<user>/Library`},
		{`two: C:\Users\a\x and D:\Users\b\y`, `two: C:\Users\<user>\x and D:\Users\<user>\y`},
		{`nothing to hide: C:\Program Files\Constella`, `nothing to hide: C:\Program Files\Constella`},
	}
	for _, tc := range tests {
		if got := Scrub(tc.in); got != tc.want {
			t.Errorf("Scrub(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLevels(t *testing.T) {
	t.Parallel()
	if !ValidLevel("warn") || ValidLevel("debug") || !Reportable("fatal") || Reportable("warn") {
		t.Error("level rules wrong")
	}
}
