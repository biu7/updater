package client

import "time"

// 业务码，与 internal/httpapi/response.go 中定义一致。
const (
	CodeOK                = 200
	CodeInvalidJSON       = 40001
	CodeInvalidService    = 40002
	CodeServiceForbidden  = 40301
	CodeJobNotFound       = 40401
	CodeJobConflict       = 40901
	CodeCreateJobFailed   = 50001
	CodeJobExecutionError = 50010
)

// JobStatus 任务状态，与服务端 jobs.Status 的 JSON 值一致。
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusSkipped   JobStatus = "skipped"
	StatusSucceeded JobStatus = "succeeded"
	StatusFailed    JobStatus = "failed"
)

// JobAction 任务动作，与服务端 jobs.Action 的 JSON 值一致。
type JobAction string

const (
	ActionUpdate  JobAction = "update"
	ActionRestart JobAction = "restart"
)

// Job 表示 GET /jobs/:id 返回的 data 中的任务信息。
type Job struct {
	ID         string     `json:"id"`
	Services   []string   `json:"services"`
	Action     JobAction  `json:"action"`
	Status     JobStatus  `json:"status"`
	Error      string     `json:"error,omitempty"`
	LogTail    string     `json:"log_tail,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Succeeded 是否为成功完成。
func (j *Job) Succeeded() bool {
	return j.Status == StatusSucceeded
}

// Failed 是否为执行失败（含超时等，需结合服务端 message / error）。
func (j *Job) Failed() bool {
	return j.Status == StatusFailed
}

// Skipped 是否为跳过（通常仅 update 动作会出现）。
func (j *Job) Skipped() bool {
	return j.Status == StatusSkipped
}

// Pending 是否仍在排队等待执行。
func (j *Job) Pending() bool {
	return j.Status == StatusPending
}

// Running 是否正在执行。
func (j *Job) Running() bool {
	return j.Status == StatusRunning
}

// Done 是否已到达终态（成功、跳过或失败）。
func (j *Job) Done() bool {
	return j.Succeeded() || j.Skipped() || j.Failed()
}

// InProgress 是否仍在进行中（pending 或 running）。
func (j *Job) InProgress() bool {
	return j.Pending() || j.Running()
}
