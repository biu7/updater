package httpapi

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"updater/internal/config"
	"updater/internal/jobs"
	"updater/internal/updater"
)

// Handlers 聚合 HTTP 处理函数依赖。
type Handlers struct {
	cfg    config.Config
	store  *jobs.Store
	runner *updater.Runner
}

// NewHandlers 创建处理器。
func NewHandlers(cfg config.Config, store *jobs.Store, runner *updater.Runner) *Handlers {
	return &Handlers{cfg: cfg, store: store, runner: runner}
}

type updateBody struct {
	Service string `json:"service"`
}

// PostUpdate 异步触发指定服务的 compose 更新。
func (h *Handlers) PostUpdate(c *gin.Context) {
	var body updateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		writeResponse(c, http.StatusBadRequest, codeInvalidJSON, "请求体格式不正确", err.Error(), nil)
		return
	}
	if err := ValidateServiceName(body.Service); err != nil {
		writeResponse(c, http.StatusBadRequest, codeInvalidService, "服务名称不合法", err.Error(), nil)
		return
	}
	if !h.cfg.IsServiceAllowed(body.Service) {
		writeResponse(c, http.StatusForbidden, codeServiceForbidden, "该服务不在允许更新范围内", "", gin.H{
			"service": body.Service,
		})
		return
	}

	j, existing, err := h.store.TryEnqueue(body.Service)
	if err != nil {
		if err == jobs.ErrConflict && existing != nil {
			writeResponse(c, http.StatusConflict, codeJobConflict, "当前服务已有更新任务正在执行", "", gin.H{
				"existing_job_id": existing.ID,
				"service":         existing.Service,
				"status":          existing.Status,
			})
			return
		}
		writeResponse(c, http.StatusInternalServerError, codeCreateJobFailed, "创建更新任务失败，请稍后重试", err.Error(), nil)
		return
	}

	log.Printf("已接受更新任务: job=%s service=%s", j.ID, j.Service)

	go h.runJob(j.ID, j.Service)

	writeResponse(c, http.StatusOK, successCode, "更新任务已创建，正在后台执行", "", gin.H{
		"job_id":  j.ID,
		"service": j.Service,
	})
}

func (h *Handlers) runJob(jobID, service string) {
	if !h.store.MarkRunning(jobID) {
		log.Printf("任务无法进入 running（可能已被清理）: job=%s", jobID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.UpdateTimeout)
	defer cancel()

	msg, logTail, err := h.runner.UpdateService(ctx, service)
	if err != nil {
		log.Printf("任务失败: job=%s service=%s err=%v", jobID, service, err)
		h.store.FinishFailed(jobID, err.Error(), logTail)
		return
	}
	log.Printf("任务成功: job=%s service=%s msg=%s", jobID, service, msg)
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
