package jobs

type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusFailed
}
