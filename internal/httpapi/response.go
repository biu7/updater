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
	Service    string      `json:"service"`
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
		Service:    j.Service,
		Status:     j.Status,
		Error:      j.Error,
		LogTail:    j.LogTail,
		CreatedAt:  j.CreatedAt,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
	}

	switch j.Status {
	case jobs.StatusPending:
		return http.StatusOK, successCode, "更新任务已创建，等待开始执行", "", data
	case jobs.StatusRunning:
		return http.StatusOK, successCode, "更新任务正在执行中", "", data
	case jobs.StatusSkipped:
		return http.StatusOK, successCode, friendlySuccessMessage(j.Message), "", data
	case jobs.StatusSucceeded:
		return http.StatusOK, successCode, friendlySuccessMessage(j.Message), "", data
	case jobs.StatusFailed:
		return http.StatusOK, codeJobExecutionError, friendlyFailureMessage(j.Error), "", data
	default:
		return http.StatusOK, successCode, "任务状态未知", "", data
	}
}

func friendlySuccessMessage(raw string) string {
	switch {
	case raw == "":
		return "更新任务执行成功"
	case isUpdatedMessage(raw):
		return "检测到新版本，更新已完成"
	case isSkippedMessage(raw):
		return "未检测到需要更新的版本，已跳过本次更新"
	case isUncertainSkippedMessage(raw):
		return "暂时无法确认是否存在可更新版本，已跳过本次更新"
	default:
		return raw
	}
}

func friendlyFailureMessage(raw string) string {
	switch {
	case strings.Contains(raw, "context deadline exceeded"):
		return "更新任务执行超时，已终止本次更新"
	case raw == "":
		return "更新任务执行失败，请稍后重试"
	default:
		return "更新任务执行失败，请稍后重试"
	}
}

func isUpdatedMessage(raw string) bool {
	return strings.Contains(raw, "更新已完成")
}

func isSkippedMessage(raw string) bool {
	return strings.Contains(raw, "未变化") || strings.Contains(raw, "未发现可更新镜像")
}

func isUncertainSkippedMessage(raw string) bool {
	return strings.Contains(raw, "无法确认")
}
