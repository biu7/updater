package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/biu7/updater/internal/config"
	"github.com/biu7/updater/internal/jobs"
	"github.com/biu7/updater/internal/updater"
)

// Handlers 聚合 HTTP 处理函数依赖。
type Handlers struct {
	cfg    config.Config
	store  *jobs.Store
	runner composeRunner
}

type composeRunner interface {
	ResolveTargetServices(ctx context.Context) ([]string, error)
	UpdateServices(ctx context.Context, services []string) (message string, log string, err error)
	RestartServices(ctx context.Context, services []string) (message string, log string, err error)
}

// NewHandlers 创建处理器。
func NewHandlers(cfg config.Config, store *jobs.Store, runner composeRunner) *Handlers {
	return &Handlers{cfg: cfg, store: store, runner: runner}
}

type updateBody struct {
	Services []string `json:"services"`
}

// PostUpdate 异步触发更新。
func (h *Handlers) PostUpdate(c *gin.Context) {
	h.enqueueServiceJob(c, jobs.ActionUpdate, "更新", "更新任务已创建，正在后台执行", h.runner.UpdateServices)
}

// PostRestart 异步触发重启。
func (h *Handlers) PostRestart(c *gin.Context) {
	h.enqueueServiceJob(c, jobs.ActionRestart, "重启", "重启任务已创建，正在后台执行", h.runner.RestartServices)
}

func (h *Handlers) enqueueServiceJob(
	c *gin.Context,
	action jobs.Action,
	actionName string,
	successMessage string,
	run func(ctx context.Context, services []string) (message string, log string, err error),
) {
	if _, err := parseUpdateBody(c); err != nil {
		writeResponse(c, http.StatusBadRequest, codeInvalidJSON, "请求体格式不正确", err.Error(), nil)
		return
	}

	services, err := h.runner.ResolveTargetServices(c.Request.Context())
	if err != nil {
		switch {
		case errors.Is(err, updater.ErrNoAllowedServices):
			writeResponse(c, http.StatusForbidden, codeServiceForbidden, "当前 compose 中没有允许执行的服务，未创建任务", "", nil)
		case errors.Is(err, updater.ErrNoComposeServices):
			writeResponse(c, http.StatusInternalServerError, codeCreateJobFailed, "当前 compose 中未解析到可执行服务，未创建任务", err.Error(), nil)
		default:
			writeResponse(c, http.StatusInternalServerError, codeCreateJobFailed, fmt.Sprintf("创建%s任务失败，请稍后重试", actionName), err.Error(), nil)
		}
		return
	}

	j, existing, err := h.store.TryEnqueueBatch(services, action)
	if err != nil {
		if err == jobs.ErrConflict && existing != nil {
			writeResponse(c, http.StatusConflict, codeJobConflict, "当前已有任务正在执行，未创建任务", "", gin.H{
				"services":           existing.Services,
				"existing_services":  existing.Services,
				"requested_services": services,
				"existing_job_id":    existing.ID,
				"action":             existing.Action,
				"status":             existing.Status,
			})
			return
		}
		writeResponse(c, http.StatusInternalServerError, codeCreateJobFailed, fmt.Sprintf("创建%s任务失败，请稍后重试", actionName), err.Error(), nil)
		return
	}

	log.Printf("已接受%s任务: job=%s services=%s", actionName, j.ID, strings.Join(j.Services, ","))

	go h.runJob(j.ID, j.Services, actionName, run)

	writeResponse(c, http.StatusOK, successCode, successMessage, "", gin.H{
		"job_id":   j.ID,
		"services": j.Services,
		"action":   j.Action,
	})
}

func (h *Handlers) runJob(
	jobID string,
	services []string,
	actionName string,
	run func(ctx context.Context, services []string) (message string, log string, err error),
) {
	if !h.store.MarkRunning(jobID) {
		log.Printf("任务无法进入 running（可能已被清理）: job=%s", jobID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.UpdateTimeout)
	defer cancel()

	msg, logTail, err := run(ctx, services)
	if err != nil {
		log.Printf("%s任务失败: job=%s services=%s err=%v", actionName, jobID, strings.Join(services, ","), err)
		h.store.FinishFailed(jobID, err.Error(), logTail)
		return
	}
	log.Printf("%s任务成功: job=%s services=%s msg=%s", actionName, jobID, strings.Join(services, ","), msg)
	if isSkippedMessage(msg) || isUncertainSkippedMessage(msg) {
		h.store.FinishSkipped(jobID, msg, logTail)
		return
	}
	h.store.FinishSucceeded(jobID, msg, logTail)
}

// GetJob 查询任务状态。
func (h *Handlers) GetJob(c *gin.Context) {
	id := c.Param("id")
	j := h.store.Get(id)
	if j == nil {
		writeResponse(c, http.StatusNotFound, codeJobNotFound, "未找到对应的更新任务", "请确认任务 ID 是否正确", nil)
		return
	}
	httpStatus, code, message, detail, data := buildJobResponse(j)
	writeResponse(c, httpStatus, code, message, detail, data)
}

// Health 健康检查。
func (h *Handlers) Health(c *gin.Context) {
	writeResponse(c, http.StatusOK, successCode, "服务运行正常", "", gin.H{"status": "ok"})
}

func parseUpdateBody(c *gin.Context) (updateBody, error) {
	var body updateBody
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return body, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return body, nil
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return updateBody{}, err
	}
	return body, nil
}
