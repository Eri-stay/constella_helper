// Package telemetry is the pure domain of client diagnostics: what may leave
// a user's machine. Paths are scrubbed of the operating-system user name.
package telemetry

import "regexp"

var userPaths = []*regexp.Regexp{
	regexp.MustCompile(`(?i)([a-z]:[\\/]+users[\\/]+)[^\\/\s"']+`), // C:\Users\name, C:/Users/name
	regexp.MustCompile(`(/home/)[^/\s"']+`),
	regexp.MustCompile(`(/Users/)[^/\s"']+`),
}

// Placeholder replaces the user name in a scrubbed path.
const Placeholder = "<user>"

// Scrub replaces the user name inside Windows, Linux and macOS home paths.
func Scrub(s string) string {
	for _, re := range userPaths {
		s = re.ReplaceAllString(s, "${1}"+Placeholder)
	}
	return s
}

// Level of a client log entry.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
	LevelFatal = "fatal"
)

// ValidLevel reports whether l is a known level.
func ValidLevel(l string) bool {
	switch l {
	case LevelInfo, LevelWarn, LevelError, LevelFatal:
		return true
	}
	return false
}

// Reportable levels are forwarded to the error sink (Sentry).
func Reportable(l string) bool { return l == LevelError || l == LevelFatal }
