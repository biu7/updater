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

type envelopeOK struct {
	OK   bool        `json:"ok"`
	Data interface{} `json:"data,omitempty"`
}

type envelopeErr struct {
	OK            bool   `json:"ok"`
	Error         string `json:"error"`
	Message       string `json:"message,omitempty"`
	ExistingJobID string `json:"existing_job_id,omitempty"`
}

// PostUpdate 异步触发指定服务的 compose 更新。
func (h *Handlers) PostUpdate(c *gin.Context) {
	var body updateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, envelopeErr{OK: false, Error: "请求体无效"})
		return
	}
	if err := ValidateServiceName(body.Service); err != nil {
		c.JSON(http.StatusBadRequest, envelopeErr{OK: false, Error: err.Error()})
		return
	}
	if !h.cfg.IsServiceAllowed(body.Service) {
		c.JSON(http.StatusForbidden, envelopeErr{OK: false, Error: "服务不在允许列表中"})
		return
	}

	j, existing, err := h.store.TryEnqueue(body.Service)
	if err != nil {
		if err == jobs.ErrConflict && existing != nil {
			c.JSON(http.StatusConflict, envelopeErr{
				OK:            false,
				Error:         "当前服务已有进行中的更新任务",
				Message:       "请等待现有任务结束后再试，或查询该任务状态",
				ExistingJobID: existing.ID,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, envelopeErr{OK: false, Error: "无法创建任务"})
		return
	}

	log.Printf("已接受更新任务: job=%s service=%s", j.ID, j.Service)

	go h.runJob(j.ID, j.Service)

	c.JSON(http.StatusAccepted, envelopeOK{
		OK: true,
		Data: gin.H{
			"job_id":  j.ID,
			"service": j.Service,
		},
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
	h.store.FinishSucceeded(jobID, msg, logTail)
}

// GetJob 查询任务状态。
func (h *Handlers) GetJob(c *gin.Context) {
	id := c.Param("id")
	j := h.store.Get(id)
	if j == nil {
		c.JSON(http.StatusNotFound, envelopeErr{OK: false, Error: "任务不存在"})
		return
	}
	c.JSON(http.StatusOK, envelopeOK{OK: true, Data: j})
}

// Health 健康检查。
func (h *Handlers) Health(c *gin.Context) {
	c.JSON(http.StatusOK, envelopeOK{OK: true, Data: gin.H{"status": "ok"}})
}
