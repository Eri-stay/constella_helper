package events

// Event types of the platform. Each one is also the routing key it is
// published with on the topic exchange.
const (
	BuildStarted   = "dataprep.build.started"
	BuildCompleted = "dataprep.build.completed"
	BuildFailed    = "dataprep.build.failed"

	BuildValidated = "catalog.build.validated"
	BuildRejected  = "catalog.build.rejected"

	ReleaseRequested = "release.requested"
	ReleasePublished = "release.published"
	ReleaseFailed    = "release.failed"

	ResearchRunCompleted = "research.run.completed"
	ResearchRunFailed    = "research.run.failed"
)
