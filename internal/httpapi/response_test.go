package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/biu7/updater/internal/jobs"
)

func TestFriendlySuccessMessage(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   string
		action jobs.Action
	}{
		{
			name:   "镜像未变化",
			raw:    "所有服务 pull 后镜像 ID 均未变化，已跳过重启",
			want:   "未检测到需要更新的版本，已跳过本次更新",
			action: jobs.ActionUpdate,
		},
		{
			name:   "成功更新",
			raw:    "更新已完成（已执行 pull 与 up -d，检测到更新的服务：api,worker）",
			want:   "更新已完成",
			action: jobs.ActionUpdate,
		},
		{
			name:   "结果不确定",
			raw:    "无法确认部分服务 pull 后镜像是否已更新，已跳过重启",
			want:   "暂时无法确认是否存在可更新版本，已跳过本次更新",
			action: jobs.ActionUpdate,
		},
		{
			name:   "成功重启",
			raw:    "重启已完成（已执行 restart）",
			want:   "重启已完成",
			action: jobs.ActionRestart,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := friendlySuccessMessage(tt.action, tt.raw); got != tt.want {
				t.Fatalf("friendlySuccessMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildJobResponseSkipped(t *testing.T) {
	now := time.Now().UTC()
	httpStatus, code, message, detail, data := buildJobResponse(&jobs.Job{
		ID:         "job-1",
		Services:   []string{"transfer", "worker"},
		Action:     jobs.ActionUpdate,
		Status:     jobs.StatusSkipped,
		Message:    "所有服务 pull 后镜像 ID 均未变化，已跳过重启",
		LogTail:    "pull log",
		CreatedAt:  now,
		FinishedAt: &now,
	})

	if httpStatus != http.StatusOK {
		t.Fatalf("httpStatus = %d, want %d", httpStatus, http.StatusOK)
	}
	if code != successCode {
		t.Fatalf("code = %d, want %d", code, successCode)
	}
	if message != "未检测到需要更新的版本，已跳过本次更新" {
		t.Fatalf("message = %q", message)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	job, ok := data.(jobData)
	if !ok {
		t.Fatalf("data type = %T, want jobData", data)
	}
	if job.Status != jobs.StatusSkipped {
		t.Fatalf("job status = %q, want %q", job.Status, jobs.StatusSkipped)
	}
	if job.Action != jobs.ActionUpdate {
		t.Fatalf("job action = %q, want %q", job.Action, jobs.ActionUpdate)
	}
	if job.LogTail != "pull log" {
		t.Fatalf("job log tail = %q, want pull log", job.LogTail)
	}
	if len(job.Services) != 2 || job.Services[0] != "transfer" || job.Services[1] != "worker" {
		t.Fatalf("job services = %#v", job.Services)
	}
}

func TestBuildJobResponseSucceeded(t *testing.T) {
	now := time.Now().UTC()
	httpStatus, code, message, detail, data := buildJobResponse(&jobs.Job{
		ID:         "job-3",
		Services:   []string{"transfer", "worker"},
		Action:     jobs.ActionRestart,
		Status:     jobs.StatusSucceeded,
		Message:    "重启已完成（已执行 restart）",
		LogTail:    "restart log",
		CreatedAt:  now,
		FinishedAt: &now,
	})

	if httpStatus != http.StatusOK {
		t.Fatalf("httpStatus = %d, want %d", httpStatus, http.StatusOK)
	}
	if code != successCode {
		t.Fatalf("code = %d, want %d", code, successCode)
	}
	if message != "重启已完成" {
		t.Fatalf("message = %q", message)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	job, ok := data.(jobData)
	if !ok {
		t.Fatalf("data type = %T, want jobData", data)
	}
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("job status = %q, want %q", job.Status, jobs.StatusSucceeded)
	}
	if job.Action != jobs.ActionRestart {
		t.Fatalf("job action = %q, want %q", job.Action, jobs.ActionRestart)
	}
}

func TestBuildJobResponseFailed(t *testing.T) {
	now := time.Now().UTC()
	httpStatus, code, message, detail, data := buildJobResponse(&jobs.Job{
		ID:         "job-2",
		Services:   []string{"transfer", "worker"},
		Action:     jobs.ActionRestart,
		Status:     jobs.StatusFailed,
		Error:      "docker compose restart: exit status 1",
		LogTail:    "error log",
		CreatedAt:  now,
		FinishedAt: &now,
	})

	if httpStatus != http.StatusOK {
		t.Fatalf("httpStatus = %d, want %d", httpStatus, http.StatusOK)
	}
	if code != codeJobExecutionError {
		t.Fatalf("code = %d, want %d", code, codeJobExecutionError)
	}
	if message != "重启任务执行失败，请稍后重试" {
		t.Fatalf("message = %q", message)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	job, ok := data.(jobData)
	if !ok {
		t.Fatalf("data type = %T, want jobData", data)
	}
	if len(job.Services) != 2 || job.Services[0] != "transfer" {
		t.Fatalf("job services = %#v", job.Services)
	}
	if job.Error != "docker compose restart: exit status 1" {
		t.Fatalf("job error = %q", job.Error)
	}
	if job.LogTail != "error log" {
		t.Fatalf("job log tail = %q", job.LogTail)
	}
}
