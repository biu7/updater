package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/biu7/updater/internal/jobs"
)

const (
	successCode           = 200
	codeInvalidJSON       = 40001
	codeInvalidService    = 40002
	codeServiceForbidden  = 40301
	codeJobConflict       = 40901
	codeJobNotFound       = 40401
	codeCreateJobFailed   = 50001
	codeJobExecutionError = 50010
)

type apiResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Detail  string      `json:"detail,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type jobData struct {
	ID         string      `json:"id"`
	Services   []string    `json:"services"`
	Action     jobs.Action `json:"action"`
	Status     jobs.Status `json:"status"`
	Error      string      `json:"error,omitempty"`
	LogTail    string      `json:"log_tail,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
}

func writeResponse(c *gin.Context, httpStatus, code int, message, detail string, data interface{}) {
	c.JSON(httpStatus, apiResponse{
		Code:    code,
		Message: message,
		Detail:  detail,
		Data:    data,
	})
}

func buildJobResponse(j *jobs.Job) (httpStatus int, code int, message string, detail string, data interface{}) {
	data = jobData{
		ID:         j.ID,
		Services:   j.Services,
		Action:     j.Action,
		Status:     j.Status,
		Error:      j.Error,
		LogTail:    j.LogTail,
		CreatedAt:  j.CreatedAt,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
	}

	switch j.Status {
	case jobs.StatusPending:
		return http.StatusOK, successCode, pendingMessage(j.Action), "", data
	case jobs.StatusRunning:
		return http.StatusOK, successCode, runningMessage(j.Action), "", data
	case jobs.StatusSkipped:
		return http.StatusOK, successCode, friendlySuccessMessage(j.Action, j.Message), "", data
	case jobs.StatusSucceeded:
		return http.StatusOK, successCode, friendlySuccessMessage(j.Action, j.Message), "", data
	case jobs.StatusFailed:
		return http.StatusOK, codeJobExecutionError, friendlyFailureMessage(j.Action, j.Error), "", data
	default:
		return http.StatusOK, successCode, "任务状态未知", "", data
	}
}

func pendingMessage(action jobs.Action) string {
	switch action {
	case jobs.ActionRestart:
		return "重启任务已创建，等待开始执行"
	default:
		return "更新任务已创建，等待开始执行"
	}
}

func runningMessage(action jobs.Action) string {
	switch action {
	case jobs.ActionRestart:
		return "重启任务正在执行中"
	default:
		return "更新任务正在执行中"
	}
}

func friendlySuccessMessage(action jobs.Action, raw string) string {
	switch {
	case raw == "":
		if action == jobs.ActionRestart {
			return "重启任务执行成功"
		}
		return "更新任务执行成功"
	case isUpdatedMessage(raw):
		return "更新已完成"
	case isRestartedMessage(raw):
		return "重启已完成"
	case isSkippedMessage(raw):
		return "未检测到需要更新的版本，已跳过本次更新"
	case isUncertainSkippedMessage(raw):
		return "暂时无法确认是否存在可更新版本，已跳过本次更新"
	default:
		return raw
	}
}

func friendlyFailureMessage(action jobs.Action, raw string) string {
	switch {
	case strings.Contains(raw, "context deadline exceeded"):
		if action == jobs.ActionRestart {
			return "重启任务执行超时，已终止本次重启"
		}
		return "更新任务执行超时，已终止本次更新"
	case raw == "":
		if action == jobs.ActionRestart {
			return "重启任务执行失败，请稍后重试"
		}
		return "更新任务执行失败，请稍后重试"
	default:
		if action == jobs.ActionRestart {
			return "重启任务执行失败，请稍后重试"
		}
		return "更新任务执行失败，请稍后重试"
	}
}

func isUpdatedMessage(raw string) bool {
	return strings.Contains(raw, "更新已完成")
}

func isRestartedMessage(raw string) bool {
	return strings.Contains(raw, "重启已完成")
}

func isSkippedMessage(raw string) bool {
	return strings.Contains(raw, "未变化") || strings.Contains(raw, "未发现可更新镜像")
}

func isUncertainSkippedMessage(raw string) bool {
	return strings.Contains(raw, "无法确认")
}
