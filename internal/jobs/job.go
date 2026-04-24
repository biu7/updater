package jobs

import "time"

// Status 任务状态。
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSkipped   Status = "skipped"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Action 表示任务动作类型。
type Action string

const (
	ActionUpdate  Action = "update"
	ActionRestart Action = "restart"
)

// Job 表示一次异步服务任务。
type Job struct {
	ID         string     `json:"id"`
	Services   []string   `json:"services"`
	Action     Action     `json:"action"`
	Status     Status     `json:"status"`
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error,omitempty"`
	LogTail    string     `json:"log_tail,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
