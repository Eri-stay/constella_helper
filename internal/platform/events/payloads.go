package events

// Payloads of the events defined in types.go. They are plain data shared by
// the producer and its consumers; the contract is documented in api/events.

// BuildStartedData is the payload of BuildStarted.
type BuildStartedData struct {
	BuildID string `json:"build_id"`
}

// BuildCompletedData is the payload of BuildCompleted. Artifact is a file
// name inside the shared artifacts volume, never a path.
type BuildCompletedData struct {
	BuildID        string `json:"build_id"`
	Artifact       string `json:"artifact"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	Stars          int    `json:"stars"`
	Constellations int    `json:"constellations"`
	Cultures       int    `json:"cultures"`
	Edges          int    `json:"edges"`
	DroppedEdges   int    `json:"dropped_edges"`
}

// BuildFailedData is the payload of BuildFailed. Code is an error code, not
// text; Detail is the technical cause for the log and the audit.
type BuildFailedData struct {
	BuildID string `json:"build_id"`
	Code    string `json:"code"`
	Detail  string `json:"detail,omitempty"`
}

// BuildValidatedData is the payload of BuildValidated.
type BuildValidatedData struct {
	BuildID  string `json:"build_id"`
	Warnings int    `json:"warnings"`
}

// BuildRejectedData is the payload of BuildRejected; FailedChecks holds check
// codes of the validation report.
type BuildRejectedData struct {
	BuildID      string   `json:"build_id"`
	FailedChecks []string `json:"failed_checks"`
}
