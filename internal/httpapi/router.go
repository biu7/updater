// Package httpapi 提供 Gin HTTP 路由与处理器。
package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/biu7/updater/internal/config"
	"github.com/biu7/updater/internal/jobs"
	"github.com/biu7/updater/internal/updater"
)

// NewRouter 注册路由。
func NewRouter(cfg config.Config, store *jobs.Store, runner *updater.Runner) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	h := NewHandlers(cfg, store, runner)
	r.GET("/health", h.Health)
	r.POST("/update", h.PostUpdate)
	r.POST("/restart", h.PostRestart)
	r.GET("/jobs/:id", h.GetJob)
	return r
}
