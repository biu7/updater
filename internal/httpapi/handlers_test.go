package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/biu7/updater/internal/config"
	"github.com/biu7/updater/internal/jobs"
	"github.com/biu7/updater/internal/updater"
)

type stubRunner struct {
	resolveFn func(ctx context.Context) ([]string, error)
	updateFn  func(ctx context.Context, services []string) (string, string, error)
	restartFn func(ctx context.Context, services []string) (string, string, error)
}

func (s *stubRunner) ResolveTargetServices(ctx context.Context) ([]string, error) {
	if s.resolveFn != nil {
		return s.resolveFn(ctx)
	}
	return nil, nil
}

func (s *stubRunner) UpdateServices(ctx context.Context, services []string) (string, string, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, services)
	}
	return "更新已完成", "", nil
}

func (s *stubRunner) RestartServices(ctx context.Context, services []string) (string, string, error) {
	if s.restartFn != nil {
		return s.restartFn(ctx, services)
	}
	return "重启已完成", "", nil
}

func TestPostUpdate_AcceptsEmptyBody(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	store := jobs.NewStore()
	runner := &stubRunner{
		resolveFn: func(ctx context.Context) ([]string, error) {
			return []string{"api", "worker"}, nil
		},
	}

	r := gin.New()
	h := NewHandlers(config.Config{UpdateTimeout: time.Second}, store, runner)
	r.POST("/update", h.PostUpdate)

	req := httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			JobID    string   `json:"job_id"`
			Services []string `json:"services"`
			Action   string   `json:"action"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Code != successCode {
		t.Fatalf("code = %d, want %d", resp.Code, successCode)
	}
	if resp.Data.JobID == "" {
		t.Fatal("job_id 不应为空")
	}
	if resp.Data.Action != string(jobs.ActionUpdate) {
		t.Fatalf("action = %q", resp.Data.Action)
	}
	if len(resp.Data.Services) != 2 || resp.Data.Services[0] != "api" || resp.Data.Services[1] != "worker" {
		t.Fatalf("services = %#v", resp.Data.Services)
	}
}

func TestPostUpdate_ForbiddenWhenNoAllowedServices(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	store := jobs.NewStore()
	runner := &stubRunner{
		resolveFn: func(ctx context.Context) ([]string, error) {
			return nil, updater.ErrNoAllowedServices
		},
	}

	r := gin.New()
	h := NewHandlers(config.Config{UpdateTimeout: time.Second}, store, runner)
	r.POST("/update", h.PostUpdate)

	req := httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestPostRestart_ConflictUsesResolvedServices(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	store := jobs.NewStore()
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	runner := &stubRunner{
		resolveFn: func(ctx context.Context) ([]string, error) {
			return []string{"api", "worker"}, nil
		},
		restartFn: func(ctx context.Context, services []string) (string, string, error) {
			started <- struct{}{}
			<-block
			return "重启已完成（已执行 restart）", "", nil
		},
	}

	r := gin.New()
	h := NewHandlers(config.Config{UpdateTimeout: time.Second}, store, runner)
	r.POST("/restart", h.PostRestart)

	req1 := httptest.NewRequest(http.MethodPost, "/restart", strings.NewReader(`{}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", w1.Code, http.StatusOK)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("首个任务未开始执行")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/restart", strings.NewReader(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	close(block)

	if w2.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want %d", w2.Code, http.StatusConflict)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Services          []string `json:"services"`
			ExistingServices  []string `json:"existing_services"`
			RequestedServices []string `json:"requested_services"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal conflict response: %v", err)
	}
	if resp.Code != codeJobConflict {
		t.Fatalf("code = %d, want %d", resp.Code, codeJobConflict)
	}
	if len(resp.Data.Services) != 2 {
		t.Fatalf("services = %#v", resp.Data.Services)
	}
	if len(resp.Data.ExistingServices) != 2 || resp.Data.ExistingServices[0] != "api" {
		t.Fatalf("existing services = %#v", resp.Data.ExistingServices)
	}
	if len(resp.Data.RequestedServices) != 2 || resp.Data.RequestedServices[1] != "worker" {
		t.Fatalf("requested services = %#v", resp.Data.RequestedServices)
	}
}
