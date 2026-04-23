package client

// JobResult 表示一次任务查询的完整解析结果（含 HTTP 与业务信封）。
type JobResult struct {
	HTTPStatus int
	Code       int
	Message    string
	Detail     string
	Job        Job
}

// Succeeded 见 Job.Succeeded。
func (r *JobResult) Succeeded() bool {
	return r.Job.Succeeded()
}

// Failed 见 Job.Failed。
func (r *JobResult) Failed() bool {
	return r.Job.Failed()
}

// Skipped 见 Job.Skipped。
func (r *JobResult) Skipped() bool {
	return r.Job.Skipped()
}

// Pending 见 Job.Pending。
func (r *JobResult) Pending() bool {
	return r.Job.Pending()
}

// Running 见 Job.Running。
func (r *JobResult) Running() bool {
	return r.Job.Running()
}

// Done 见 Job.Done。
func (r *JobResult) Done() bool {
	return r.Job.Done()
}

// InProgress 见 Job.InProgress。
func (r *JobResult) InProgress() bool {
	return r.Job.InProgress()
}

// TerminalFailure 表示任务已结束且结果为失败（含 HTTP 200 但 code=50010 的情况）。
func (r *JobResult) TerminalFailure() bool {
	return r.Done() && r.Failed()
}

// CreateUpdateResult 表示 POST /update 或 POST /restart 的解析结果。
type CreateUpdateResult struct {
	HTTPStatus int
	Code       int
	Message    string
	Detail     string

	// 创建成功时（code=200 且 HTTP 200）
	JobID   string
	Service string
	Action  JobAction

	// 409 冲突时
	ExistingJobID   string
	ExistingService string
	ExistingAction  JobAction
	ExistingStatus  JobStatus
}

// Created 是否已成功创建异步任务（可拿到 job_id）。
func (r *CreateUpdateResult) Created() bool {
	return r.Code == CodeOK && r.HTTPStatus == 200 && r.JobID != ""
}

// Conflict 是否为同服务已有进行中任务（409 / code=40901）。
func (r *CreateUpdateResult) Conflict() bool {
	return r.Code == CodeJobConflict || r.HTTPStatus == 409
}

// Forbidden 是否因服务不在白名单被拒绝（403 / code=40301）。
func (r *CreateUpdateResult) Forbidden() bool {
	return r.Code == CodeServiceForbidden || r.HTTPStatus == 403
}

// HealthResult 表示 GET /health 的解析结果。
type HealthResult struct {
	HTTPStatus int
	Code       int
	Message    string
	Detail     string
	Status     string // data.status，正常为 "ok"
}

// OK 服务是否健康（HTTP 200、业务码 200 且 data.status 为 ok）。
func (h *HealthResult) OK() bool {
	return h.Code == CodeOK && h.HTTPStatus == 200 && h.Status == "ok"
}
