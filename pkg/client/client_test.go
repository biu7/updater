package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/biu7/updater/pkg/client"
)

func TestHealth_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200, "message": "服务运行正常", "data": map[string]string{"status": "ok"},
		})
	}))
	defer srv.Close()

	c, err := client.NewWithBaseURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !h.OK() {
		t.Fatalf("期望健康，得到 HTTP=%d code=%d status=%q", h.HTTPStatus, h.Code, h.Status)
	}
}

func TestUpdate_Created(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200, "message": "更新任务已创建，正在后台执行",
			"data": map[string]string{"job_id": "j1", "service": "api"},
		})
	}))
	defer srv.Close()

	c, _ := client.NewWithBaseURL(srv.URL)
	res, err := c.Update(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created() || res.JobID != "j1" || res.Service != "api" {
		t.Fatalf("unexpected: %+v", res)
	}
	if res.Conflict() {
		t.Fatal("不应为冲突")
	}
}

func TestUpdate_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 40901, "message": "当前服务已有更新任务正在执行",
			"data": map[string]interface{}{
				"existing_job_id": "old", "service": "api", "status": "running",
			},
		})
	}))
	defer srv.Close()

	c, _ := client.NewWithBaseURL(srv.URL)
	res, err := c.Update(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Conflict() || res.ExistingJobID != "old" || res.ExistingStatus != client.StatusRunning {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestGetJob_FailedHTTP200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": client.CodeJobExecutionError, "message": "更新任务执行失败，请稍后重试",
			"data": map[string]interface{}{
				"id": "j1", "service": "api", "status": "failed", "error": "boom",
			},
		})
	}))
	defer srv.Close()

	c, _ := client.NewWithBaseURL(srv.URL)
	res, err := c.GetJob(context.Background(), "j1")
	if err != nil {
		t.Fatal(err)
	}
	if res.HTTPStatus != 200 || res.Code != client.CodeJobExecutionError {
		t.Fatalf("HTTP/code: %+v", res)
	}
	if !res.Failed() || !res.Done() || res.Succeeded() {
		t.Fatalf("状态判定错误: %+v", res)
	}
	if !res.TerminalFailure() {
		t.Fatal("TerminalFailure 应为 true")
	}
}

func TestJobResult_Skipped(t *testing.T) {
	j := client.Job{Status: client.StatusSkipped}
	if !j.Skipped() || !j.Done() || j.Succeeded() {
		t.Fatal(j)
	}
}

func TestWaitJob(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 2 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 200, "message": "执行中",
				"data": map[string]interface{}{
					"id": "j1", "service": "api", "status": "running",
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200, "message": "完成",
			"data": map[string]interface{}{
				"id": "j1", "service": "api", "status": "succeeded",
			},
		})
	}))
	defer srv.Close()

	c, _ := client.NewWithBaseURL(srv.URL)
	ctx := context.Background()
	res, err := c.WaitJob(ctx, "j1", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.Done() {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestNew_EmptyBaseURL(t *testing.T) {
	_, err := client.New(client.Config{BaseURL: "  "})
	if err == nil {
		t.Fatal("期望错误")
	}
}
