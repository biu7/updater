package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/biu7/updater/internal/config"
	"github.com/biu7/updater/internal/jobs"
	"github.com/biu7/updater/internal/updater"
)

type stubRunner struct {
	mu        sync.Mutex
	allowed   []string // compose 服务 ∩ ALLOWED_SERVICES
	updateFn  func(ctx context.Context, services []string, pull bool) (updater.Result, error)
	pullFn    func(ctx context.Context, services []string) (updater.Result, error)
	restartFn func(ctx context.Context, services []string) (string, string, error)
	checkFn   func(ctx context.Context, services []string) []updater.CheckResult
	calls     []string
}

func (s *stubRunner) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *stubRunner) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *stubRunner) ResolveTargetServices(ctx context.Context) ([]string, error) {
	if len(s.allowed) == 0 {
		return nil, updater.ErrNoAllowedServices
	}
	return s.allowed, nil
}

func (s *stubRunner) ValidateServices(ctx context.Context, services []string) error {
	for _, svc := range services {
		if !slices.Contains(s.allowed, svc) {
			return fmt.Errorf("%w: %s", updater.ErrInvalidServices, svc)
		}
	}
	return nil
}

func (s *stubRunner) UpdateServices(ctx context.Context, services []string, pull bool) (updater.Result, error) {
	s.record(fmt.Sprintf("update %s pull=%t", strings.Join(services, ","), pull))
	if s.updateFn != nil {
		return s.updateFn(ctx, services, pull)
	}
	return updater.Result{Message: "更新已完成", NeedsUp: services, Upped: services, Uncertain: []string{}}, nil
}

func (s *stubRunner) PullServices(ctx context.Context, services []string) (updater.Result, error) {
	s.record("pull " + strings.Join(services, ","))
	if s.pullFn != nil {
		return s.pullFn(ctx, services)
	}
	return updater.Result{Message: "拉取已完成", NeedsUp: services, Upped: []string{}, Uncertain: []string{}}, nil
}

func (s *stubRunner) RestartServices(ctx context.Context, services []string) (string, string, error) {
	s.record("restart " + strings.Join(services, ","))
	if s.restartFn != nil {
		return s.restartFn(ctx, services)
	}
	return "重启已完成（已执行 restart）", "", nil
}

func (s *stubRunner) CheckServices(ctx context.Context, services []string) []updater.CheckResult {
	s.record("check " + strings.Join(services, ","))
	if s.checkFn != nil {
		return s.checkFn(ctx, services)
	}
	return nil
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Detail  string          `json:"detail"`
	Data    json.RawMessage `json:"data"`
}

func newTestRouter(runner *stubRunner) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	h := NewHandlers(config.Config{UpdateTimeout: 5 * time.Second}, jobs.NewStore(), runner)
	r := gin.New()
	r.POST("/update", h.PostUpdate)
	r.POST("/pull", h.PostPull)
	r.POST("/check", h.PostCheck)
	r.POST("/restart", h.PostRestart)
	r.GET("/jobs/latest", h.GetLatestJob)
	r.GET("/jobs/:id", h.GetJob)
	return r
}

func do(t *testing.T, r http.Handler, method, path, body string) (int, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal %s: %v", w.Body.String(), err)
	}
	return w.Code, env
}

func waitDone(t *testing.T, r http.Handler, jobID string) jobData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, env := do(t, r, http.MethodGet, "/jobs/"+jobID, "")
		var j jobData
		if err := json.Unmarshal(env.Data, &j); err != nil {
			t.Fatal(err)
		}
		if j.Status != jobs.StatusPending && j.Status != jobs.StatusRunning {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return jobData{}
}

func createdJobID(t *testing.T, env envelope) string {
	t.Helper()
	var d struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil || d.JobID == "" {
		t.Fatalf("no job id: %s", env.Data)
	}
	return d.JobID
}

func TestServiceValidation_RejectsMissingOrDisallowedServices(t *testing.T) {
	runner := &stubRunner{allowed: []string{"trader", "ib-gateway"}}
	r := newTestRouter(runner)
	for _, path := range []string{"/update", "/pull", "/check"} {
		bodies := []string{`{"services":["db"]}`, `{"services":["trader","db"]}`, `{"services":["-bad"]}`, `{"services":[""]}`}
		if path == "/check" {
			bodies = append(bodies, ``, `{}`, `{"services":[]}`)
		}
		for _, body := range bodies {
			code, env := do(t, r, http.MethodPost, path, body)
			if code != http.StatusBadRequest || env.Code != codeInvalidService {
				t.Fatalf("%s %q: status=%d code=%d, want 400/%d", path, body, code, env.Code, codeInvalidService)
			}
		}
		code, env := do(t, r, http.MethodPost, path, `{"services":`)
		if code != http.StatusBadRequest || env.Code != codeInvalidJSON {
			t.Fatalf("%s bad json: status=%d code=%d", path, code, env.Code)
		}
	}
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("runner should not be called: %v", calls)
	}
}

func TestPostUpdate_DefaultPullTrueAndStructuredResult(t *testing.T) {
	runner := &stubRunner{allowed: []string{"trader", "ib-gateway"}}
	r := newTestRouter(runner)
	code, env := do(t, r, http.MethodPost, "/update", `{"services":["ib-gateway"," ib-gateway "]}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, env.Data)
	}
	j := waitDone(t, r, createdJobID(t, env))
	if j.Status != jobs.StatusSucceeded || !slices.Equal(j.Services, []string{"ib-gateway"}) ||
		!slices.Equal(j.Upped, []string{"ib-gateway"}) || !slices.Equal(j.NeedsUp, []string{"ib-gateway"}) || j.Uncertain == nil {
		t.Fatalf("job = %+v", j)
	}
	if calls := runner.recorded(); !slices.Equal(calls, []string{"update ib-gateway pull=true"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestPostUpdate_PullFalseAndSkippedWhenNothingUpped(t *testing.T) {
	runner := &stubRunner{
		allowed: []string{"trader"},
		updateFn: func(ctx context.Context, services []string, pull bool) (updater.Result, error) {
			return updater.Result{Message: "无法确认部分服务是否需要更新，已跳过重启", NeedsUp: []string{}, Upped: []string{}, Uncertain: []string{"trader"}}, nil
		},
	}
	r := newTestRouter(runner)
	_, env := do(t, r, http.MethodPost, "/update", `{"services":["trader"],"pull":false}`)
	j := waitDone(t, r, createdJobID(t, env))
	if j.Status != jobs.StatusSkipped || !slices.Equal(j.Uncertain, []string{"trader"}) || len(j.Upped) != 0 {
		t.Fatalf("job = %+v", j)
	}
	if calls := runner.recorded(); !slices.Equal(calls, []string{"update trader pull=false"}) {
		t.Fatalf("calls = %v", calls)
	}
	raw, _ := json.Marshal(j)
	if !strings.Contains(string(raw), `"upped":[]`) {
		t.Fatalf("upped should serialize as []: %s", raw)
	}
}

func TestPostUpdate_FailedJobKeepsStructuredFields(t *testing.T) {
	runner := &stubRunner{
		allowed: []string{"trader"},
		updateFn: func(ctx context.Context, services []string, pull bool) (updater.Result, error) {
			return updater.Result{NeedsUp: []string{"trader"}}, fmt.Errorf("docker compose up -d: boom")
		},
	}
	r := newTestRouter(runner)
	_, env := do(t, r, http.MethodPost, "/update", `{"services":["trader"]}`)
	j := waitDone(t, r, createdJobID(t, env))
	if j.Status != jobs.StatusFailed || !slices.Equal(j.NeedsUp, []string{"trader"}) || j.Upped == nil || len(j.Upped) != 0 {
		t.Fatalf("job = %+v", j)
	}
}

func TestPostPull_PullOnlyJob(t *testing.T) {
	runner := &stubRunner{
		allowed: []string{"trader", "ib-gateway"},
		pullFn: func(ctx context.Context, services []string) (updater.Result, error) {
			return updater.Result{Message: "拉取已完成", NeedsUp: []string{}, Uncertain: []string{"trader"}}, nil
		},
	}
	r := newTestRouter(runner)
	_, env := do(t, r, http.MethodPost, "/pull", `{"services":["trader"]}`)
	j := waitDone(t, r, createdJobID(t, env))
	if j.Action != jobs.ActionPull || j.Status != jobs.StatusSucceeded || !slices.Equal(j.Uncertain, []string{"trader"}) || len(j.NeedsUp) != 0 {
		t.Fatalf("job = %+v", j)
	}
	if calls := runner.recorded(); !slices.Equal(calls, []string{"pull trader"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestPostCheck_SynchronousAndNotBlockedByRunningJob(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	runner := &stubRunner{
		allowed: []string{"trader", "ib-gateway"},
		updateFn: func(ctx context.Context, services []string, pull bool) (updater.Result, error) {
			close(started)
			<-block
			return updater.Result{}, nil
		},
		checkFn: func(ctx context.Context, services []string) []updater.CheckResult {
			if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > checkTimeout {
				t.Errorf("check ctx deadline missing or too long")
			}
			f := false
			return []updater.CheckResult{
				{Service: "trader", Image: "ghcr.io/x/trader:latest", CurrentVersion: "v1", RemoteVersion: "v1", UpdateAvailable: &f},
				{Service: "ib-gateway", Image: "ghcr.io/g/ibg:stable", CurrentVersion: "10.49", ErrorCode: updater.CheckErrRemoteAuth},
			}
		},
	}
	r := newTestRouter(runner)
	do(t, r, http.MethodPost, "/update", `{"services":["trader"]}`)
	<-started
	defer close(block)

	code, env := do(t, r, http.MethodPost, "/check", `{"services":["trader","ib-gateway"]}`)
	if code != http.StatusOK || env.Code != successCode {
		t.Fatalf("status = %d code = %d", code, env.Code)
	}
	want := `{"results":[{"service":"trader","image":"ghcr.io/x/trader:latest","current_version":"v1","remote_version":"v1","update_available":false},` +
		`{"service":"ib-gateway","image":"ghcr.io/g/ibg:stable","current_version":"10.49","remote_version":"","update_available":null,"error_code":"remote_auth_failed"}]}`
	if string(env.Data) != want {
		t.Fatalf("data = %s\nwant  %s", env.Data, want)
	}

	// 同一时刻的 update/pull 仍受单任务锁约束。
	code, env = do(t, r, http.MethodPost, "/pull", `{"services":["ib-gateway"]}`)
	if code != http.StatusConflict || env.Code != codeJobConflict {
		t.Fatalf("pull during update: status=%d code=%d", code, env.Code)
	}
}

func TestGetLatestJob(t *testing.T) {
	runner := &stubRunner{allowed: []string{"trader", "ib-gateway"}}
	r := newTestRouter(runner)

	code, env := do(t, r, http.MethodGet, "/jobs/latest?service=trader", "")
	if code != http.StatusNotFound || env.Code != codeJobNotFound {
		t.Fatalf("empty: status=%d code=%d", code, env.Code)
	}
	if code, _ := do(t, r, http.MethodGet, "/jobs/latest", ""); code != http.StatusBadRequest {
		t.Fatalf("missing service: status=%d", code)
	}

	_, env = do(t, r, http.MethodPost, "/update", `{"services":["trader"]}`)
	first := createdJobID(t, env)
	waitDone(t, r, first)
	_, env = do(t, r, http.MethodPost, "/pull", `{"services":["ib-gateway"]}`)
	second := createdJobID(t, env)
	waitDone(t, r, second)

	_, env = do(t, r, http.MethodGet, "/jobs/latest?service=trader", "")
	var j jobData
	_ = json.Unmarshal(env.Data, &j)
	if j.ID != first || j.Action != jobs.ActionUpdate || !slices.Equal(j.Upped, []string{"trader"}) {
		t.Fatalf("latest trader = %+v", j)
	}
	_, env = do(t, r, http.MethodGet, "/jobs/latest?service=ib-gateway", "")
	_ = json.Unmarshal(env.Data, &j)
	if j.ID != second || j.Action != jobs.ActionPull {
		t.Fatalf("latest ib-gateway = %+v", j)
	}
	if code, _ := do(t, r, http.MethodGet, "/jobs/"+first, ""); code != http.StatusOK {
		t.Fatalf("get job status = %d", code)
	}
}

func TestPostRestart_UnchangedUsesAllTargetServices(t *testing.T) {
	runner := &stubRunner{allowed: []string{"api", "worker"}}
	r := newTestRouter(runner)
	code, env := do(t, r, http.MethodPost, "/restart", `{"services":["ignored"]}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	j := waitDone(t, r, createdJobID(t, env))
	if j.Status != jobs.StatusSucceeded || !slices.Equal(j.Services, []string{"api", "worker"}) {
		t.Fatalf("job = %+v", j)
	}

	r2 := newTestRouter(&stubRunner{})
	if code, _ := do(t, r2, http.MethodPost, "/restart", ``); code != http.StatusForbidden {
		t.Fatalf("no allowed services: status = %d", code)
	}
}

func TestPostRestart_ConflictWhileUpdateRunning(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	runner := &stubRunner{
		allowed: []string{"api", "worker"},
		updateFn: func(ctx context.Context, services []string, pull bool) (updater.Result, error) {
			close(started)
			<-block
			return updater.Result{}, nil
		},
	}
	r := newTestRouter(runner)
	do(t, r, http.MethodPost, "/update", `{"services":["api"]}`)
	<-started
	code, env := do(t, r, http.MethodPost, "/restart", `{}`)
	close(block)
	if code != http.StatusConflict {
		t.Fatalf("status = %d", code)
	}
	var d struct {
		ExistingServices  []string `json:"existing_services"`
		RequestedServices []string `json:"requested_services"`
	}
	_ = json.Unmarshal(env.Data, &d)
	if !slices.Equal(d.ExistingServices, []string{"api"}) || !slices.Equal(d.RequestedServices, []string{"api", "worker"}) {
		t.Fatalf("conflict data = %s", env.Data)
	}
}

// 路由注册（含 /jobs/latest 与 /jobs/:id 共存）不应 panic。
func TestNewRouterRegistersRoutes(t *testing.T) {
	r := NewRouter(config.Config{UpdateTimeout: time.Second}, jobs.NewStore(), updater.NewRunner(config.Config{}))
	code, _ := do(t, r, http.MethodGet, "/jobs/latest?service=trader", "")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}
}

// 兼容旧客户端（如 pkg/client v0.0.3 发送 {}）：未传或传空 services 时目标为 compose 服务 ∩ ALLOWED_SERVICES。
func TestUpdateAndPull_EmptyServicesMeansAllTargets(t *testing.T) {
	runner := &stubRunner{allowed: []string{"ib-gateway", "trader"}}
	r := newTestRouter(runner)
	for _, tc := range []struct{ path, body, call string }{
		{"/update", `{}`, "update ib-gateway,trader pull=true"},
		{"/update", ``, "update ib-gateway,trader pull=true"},
		{"/update", `{"services":[],"pull":false}`, "update ib-gateway,trader pull=false"},
		{"/pull", `{}`, "pull ib-gateway,trader"},
	} {
		before := len(runner.recorded())
		code, env := do(t, r, http.MethodPost, tc.path, tc.body)
		if code != http.StatusOK {
			t.Fatalf("%s %q: status = %d", tc.path, tc.body, code)
		}
		j := waitDone(t, r, createdJobID(t, env))
		if !slices.Equal(j.Services, []string{"ib-gateway", "trader"}) {
			t.Fatalf("services = %v", j.Services)
		}
		if calls := runner.recorded(); len(calls) != before+1 || calls[before] != tc.call {
			t.Fatalf("calls = %v, want last %q", calls, tc.call)
		}
	}

	r2 := newTestRouter(&stubRunner{})
	for _, path := range []string{"/update", "/pull"} {
		if code, env := do(t, r2, http.MethodPost, path, `{}`); code != http.StatusForbidden || env.Code != codeServiceForbidden {
			t.Fatalf("%s no allowed services: status=%d code=%d", path, code, env.Code)
		}
	}
}
