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
	"time"

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
	ValidateServices(ctx context.Context, services []string) error
	UpdateServices(ctx context.Context, services []string, pull bool) (updater.Result, error)
	PullServices(ctx context.Context, services []string) (updater.Result, error)
	RestartServices(ctx context.Context, services []string) (message string, log string, err error)
	CheckServices(ctx context.Context, services []string) []updater.CheckResult
}

// checkTimeout 为 POST /check 的整体期限。
const checkTimeout = 60 * time.Second

// jobRunFunc 执行任务并返回结构化结果。
type jobRunFunc func(ctx context.Context, services []string) (updater.Result, error)

// NewHandlers 创建处理器。
func NewHandlers(cfg config.Config, store *jobs.Store, runner composeRunner) *Handlers {
	return &Handlers{cfg: cfg, store: store, runner: runner}
}

type updateBody struct {
	Services []string `json:"services"`
	Pull     *bool    `json:"pull"`
}

// PostUpdate 异步触发指定服务的更新：可选 pull（默认 true），再只对 needs_up 服务执行 up -d --no-deps。
func (h *Handlers) PostUpdate(c *gin.Context) {
	body, services, ok := h.parseRequestedServices(c)
	if !ok {
		return
	}
	pull := body.Pull == nil || *body.Pull
	h.enqueue(c, services, jobs.ActionUpdate, "更新", "更新任务已创建，正在后台执行",
		func(ctx context.Context, services []string) (updater.Result, error) {
			return h.runner.UpdateServices(ctx, services, pull)
		})
}

// PostPull 异步触发指定服务的 pull-only 任务，结果返回 needs_up / uncertain。
func (h *Handlers) PostPull(c *gin.Context) {
	_, services, ok := h.parseRequestedServices(c)
	if !ok {
		return
	}
	h.enqueue(c, services, jobs.ActionPull, "拉取", "拉取任务已创建，正在后台执行", h.runner.PullServices)
}

// PostCheck 同步检查指定服务是否有可用更新；不占用任务锁。
func (h *Handlers) PostCheck(c *gin.Context) {
	// 期限覆盖服务校验（compose config）与检查本身。
	ctx, cancel := context.WithTimeout(c.Request.Context(), checkTimeout)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	_, services, ok := h.parseRequestedServices(c)
	if !ok {
		return
	}
	results := h.runner.CheckServices(ctx, services)
	writeResponse(c, http.StatusOK, successCode, "检查完成", "", gin.H{"results": results})
}

// PostRestart 异步触发重启（目标为 compose 服务 ∩ ALLOWED_SERVICES 全部服务，忽略请求体中的 services）。
func (h *Handlers) PostRestart(c *gin.Context) {
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
			writeResponse(c, http.StatusInternalServerError, codeCreateJobFailed, "创建重启任务失败，请稍后重试", err.Error(), nil)
		}
		return
	}
	h.enqueue(c, services, jobs.ActionRestart, "重启", "重启任务已创建，正在后台执行",
		func(ctx context.Context, services []string) (updater.Result, error) {
			msg, logTail, err := h.runner.RestartServices(ctx, services)
			return updater.Result{Message: msg, Log: logTail}, err
		})
}

// parseRequestedServices 解析请求体并校验 services ⊆ compose 服务 ∩ ALLOWED_SERVICES；失败时已写出响应。
func (h *Handlers) parseRequestedServices(c *gin.Context) (updateBody, []string, bool) {
	body, err := parseUpdateBody(c)
	if err != nil {
		writeResponse(c, http.StatusBadRequest, codeInvalidJSON, "请求体格式不正确", err.Error(), nil)
		return body, nil, false
	}
	services, err := NormalizeServices(body.Services)
	if err != nil {
		writeResponse(c, http.StatusBadRequest, codeInvalidService, "services 参数不合法", err.Error(), nil)
		return body, nil, false
	}
	if err := h.runner.ValidateServices(c.Request.Context(), services); err != nil {
		if errors.Is(err, updater.ErrInvalidServices) {
			writeResponse(c, http.StatusBadRequest, codeInvalidService, "services 包含不存在或不允许的服务", err.Error(), nil)
		} else {
			writeResponse(c, http.StatusInternalServerError, codeCreateJobFailed, "解析 compose 服务失败，请稍后重试", err.Error(), nil)
		}
		return body, nil, false
	}
	return body, services, true
}

func (h *Handlers) enqueue(c *gin.Context, services []string, action jobs.Action, actionName, successMessage string, run jobRunFunc) {
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
	go h.runJob(j.ID, j.Services, j.Action, actionName, run)

	writeResponse(c, http.StatusOK, successCode, successMessage, "", gin.H{
		"job_id":   j.ID,
		"services": j.Services,
		"action":   j.Action,
	})
}

func (h *Handlers) runJob(jobID string, services []string, action jobs.Action, actionName string, run jobRunFunc) {
	if !h.store.MarkRunning(jobID) {
		log.Printf("任务无法进入 running（可能已被清理）: job=%s", jobID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.UpdateTimeout)
	defer cancel()

	res, err := run(ctx, services)
	outcome := jobs.Outcome{
		Message:   res.Message,
		LogTail:   res.Log,
		NeedsUp:   res.NeedsUp,
		Upped:     res.Upped,
		Uncertain: res.Uncertain,
	}
	if err != nil {
		log.Printf("%s任务失败: job=%s services=%s err=%v", actionName, jobID, strings.Join(services, ","), err)
		outcome.Error = err.Error()
		h.store.Finish(jobID, jobs.StatusFailed, outcome)
		return
	}
	log.Printf("%s任务成功: job=%s services=%s msg=%s", actionName, jobID, strings.Join(services, ","), res.Message)
	status := jobs.StatusSucceeded
	if action == jobs.ActionUpdate && len(res.Upped) == 0 {
		status = jobs.StatusSkipped
	}
	h.store.Finish(jobID, status, outcome)
}

// GetLatestJob 返回包含指定服务的最近一个任务。
func (h *Handlers) GetLatestJob(c *gin.Context) {
	service := strings.TrimSpace(c.Query("service"))
	if err := ValidateServiceName(service); err != nil {
		writeResponse(c, http.StatusBadRequest, codeInvalidService, "service 参数不合法", err.Error(), nil)
		return
	}
	j := h.store.Latest(service)
	if j == nil {
		writeResponse(c, http.StatusNotFound, codeJobNotFound, "未找到包含该服务的任务", "", nil)
		return
	}
	httpStatus, code, message, detail, data := buildJobResponse(j)
	writeResponse(c, httpStatus, code, message, detail, data)
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
