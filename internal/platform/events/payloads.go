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

// ReleaseData is the payload of the release events (requested, published,
// failed). Fields that do not apply to the event stay empty.
type ReleaseData struct {
	ReleaseID   string `json:"release_id"`
	BuildID     string `json:"build_id"`
	Mode        string `json:"mode"`
	Version     string `json:"version,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Step        string `json:"step,omitempty"`
	Code        string `json:"code,omitempty"`
	Compensated bool   `json:"compensated,omitempty"`
}

// ResearchCompletedData is the payload of ResearchRunCompleted (one sweep).
type ResearchCompletedData struct {
	SweepID      string  `json:"sweep_id"`
	BuildID      string  `json:"build_id"`
	Culture      string  `json:"culture"`
	Runs         int     `json:"runs"`
	BestStrategy string  `json:"best_strategy,omitempty"`
	BestJaccard  float64 `json:"best_jaccard"`
}

// ResearchFailedData is the payload of ResearchRunFailed.
type ResearchFailedData struct {
	SweepID string `json:"sweep_id"`
	Code    string `json:"code"`
	Detail  string `json:"detail,omitempty"`
}
