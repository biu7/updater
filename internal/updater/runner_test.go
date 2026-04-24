package updater

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/biu7/updater/internal/config"
)

func TestRunnerUpdateService_SkipWhenImageIDUnchanged(t *testing.T) {
	var gotArgs [][]string
	idCalls := 0
	r := &Runner{
		cfg: config.Config{
			ComposeFiles: []string{"docker-compose.yml", "override.yml"},
		},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"web": map[string]any{"image": "repo/web:latest"},
				},
			}, nil
		},
		localImageIDFn: func(ctx context.Context, imageRef string) (string, error) {
			if imageRef != "repo/web:latest" {
				t.Fatalf("unexpected image ref: %s", imageRef)
			}
			idCalls++
			return "sha256:same", nil
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			_, _ = io.WriteString(logSink, "pull output\n")
			return nil
		},
	}

	msg, logTail, err := r.UpdateServices(context.Background(), []string{"web"})
	if err != nil {
		t.Fatalf("UpdateService() error = %v", err)
	}
	if msg != "所有服务 pull 后镜像 ID 均未变化，已跳过重启" {
		t.Fatalf("unexpected message: %q", msg)
	}
	if idCalls != 2 {
		t.Fatalf("localImageID calls = %d, want 2", idCalls)
	}
	wantArgs := [][]string{
		{"compose", "-f", "docker-compose.yml", "-f", "override.yml", "pull", "web"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !strings.Contains(logTail, "pull output") {
		t.Fatalf("log tail should contain pull output: %q", logTail)
	}
	if !strings.Contains(logTail, "本次更新目标服务: web") {
		t.Fatalf("log tail should contain target services: %q", logTail)
	}
}

func TestRunnerUpdateService_RunUpWhenImageIDChanged(t *testing.T) {
	var gotArgs [][]string
	ids := []string{"sha256:old", "sha256:new"}
	r := &Runner{
		cfg: config.Config{},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"api": map[string]any{"image": "repo/api:latest"},
				},
			}, nil
		},
		localImageIDFn: func(ctx context.Context, imageRef string) (string, error) {
			if imageRef != "repo/api:latest" {
				t.Fatalf("unexpected image ref: %s", imageRef)
			}
			if len(ids) == 0 {
				t.Fatal("localImageID called too many times")
			}
			id := ids[0]
			ids = ids[1:]
			return id, nil
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			if len(gotArgs) == 1 {
				_, _ = io.WriteString(logSink, "pull output\n")
			} else {
				_, _ = io.WriteString(logSink, "up output\n")
			}
			return nil
		},
	}

	msg, logTail, err := r.UpdateServices(context.Background(), []string{"api", "worker"})
	if err != nil {
		t.Fatalf("UpdateService() error = %v", err)
	}
	if msg != "更新已完成（已执行 pull 与 up -d，检测到更新的服务：api）" {
		t.Fatalf("unexpected message: %q", msg)
	}
	wantArgs := [][]string{
		{"compose", "pull", "api", "worker"},
		{"compose", "up", "-d", "api", "worker"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !strings.Contains(logTail, "pull output") || !strings.Contains(logTail, "up output") {
		t.Fatalf("log tail should contain pull and up output: %q", logTail)
	}
	if !strings.Contains(logTail, "本次更新目标服务: api,worker") {
		t.Fatalf("log tail should contain target services: %q", logTail)
	}
}

func TestRunnerUpdateService_SkipBuildOnlyServiceByPullOutput(t *testing.T) {
	var gotArgs [][]string
	r := &Runner{
		cfg: config.Config{},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"worker": map[string]any{
						"build": map[string]any{"context": "."},
					},
				},
			}, nil
		},
		localImageIDFn: func(ctx context.Context, imageRef string) (string, error) {
			t.Fatalf("build-only service should not inspect image ID")
			return "", nil
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			_, _ = io.WriteString(logSink, "service worker was skipped because it has no image to be pulled\n")
			return nil
		},
	}

	msg, logTail, err := r.UpdateServices(context.Background(), []string{"worker"})
	if err != nil {
		t.Fatalf("UpdateService() error = %v", err)
	}
	if msg != "本次 pull 未发现可更新镜像（输出判定），已跳过重启" {
		t.Fatalf("unexpected message: %q", msg)
	}
	wantArgs := [][]string{
		{"compose", "pull", "worker"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !strings.Contains(logTail, "no image to be pulled") {
		t.Fatalf("log tail should contain build-only pull output: %q", logTail)
	}
}

func TestRunnerUpdateService_SkipWhenImageIDCannotBeConfirmed(t *testing.T) {
	var gotArgs [][]string
	idCalls := 0
	r := &Runner{
		cfg: config.Config{},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"api": map[string]any{"image": "repo/api:latest"},
				},
			}, nil
		},
		localImageIDFn: func(ctx context.Context, imageRef string) (string, error) {
			idCalls++
			if idCalls == 1 {
				return "sha256:old", nil
			}
			return "", fmt.Errorf("inspect failed")
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			_, _ = io.WriteString(logSink, "pull output\n")
			return nil
		},
	}

	msg, _, err := r.UpdateServices(context.Background(), []string{"api"})
	if err != nil {
		t.Fatalf("UpdateService() error = %v", err)
	}
	if msg != "无法确认部分服务 pull 后镜像是否已更新，已跳过重启" {
		t.Fatalf("unexpected message: %q", msg)
	}
	wantArgs := [][]string{
		{"compose", "pull", "api"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestRunnerUpdateService_SkipWhenPullOutputIsUncertain(t *testing.T) {
	var gotArgs [][]string
	r := &Runner{
		cfg: config.Config{},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return nil, fmt.Errorf("config unavailable")
		},
		localImageIDFn: func(ctx context.Context, imageRef string) (string, error) {
			t.Fatalf("service without confirmed image ref should not inspect image ID")
			return "", nil
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			_, _ = io.WriteString(logSink, "some ambiguous pull output\n")
			return nil
		},
	}

	msg, logTail, err := r.UpdateServices(context.Background(), []string{"worker"})
	if err != nil {
		t.Fatalf("UpdateService() error = %v", err)
	}
	if msg != "无法确认本次 pull 是否已拉取到新镜像，已跳过重启" {
		t.Fatalf("unexpected message: %q", msg)
	}
	wantArgs := [][]string{
		{"compose", "pull", "worker"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !strings.Contains(logTail, "ambiguous pull output") {
		t.Fatalf("log tail should contain pull output: %q", logTail)
	}
}

func TestRunnerRestartServices(t *testing.T) {
	var gotArgs [][]string
	r := &Runner{
		cfg: config.Config{
			ComposeFiles: []string{"docker-compose.yml"},
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			_, _ = io.WriteString(logSink, "restart output\n")
			return nil
		},
	}

	msg, logTail, err := r.RestartServices(context.Background(), []string{"api", "worker"})
	if err != nil {
		t.Fatalf("RestartService() error = %v", err)
	}
	if msg != "重启已完成（已执行 restart）" {
		t.Fatalf("unexpected message: %q", msg)
	}
	wantArgs := [][]string{
		{"compose", "-f", "docker-compose.yml", "restart", "api", "worker"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !strings.Contains(logTail, "restart output") {
		t.Fatalf("log tail should contain restart output: %q", logTail)
	}
	if !strings.Contains(logTail, "本次重启目标服务: api,worker") {
		t.Fatalf("log tail should contain target services: %q", logTail)
	}
}

func TestRunnerRestartService_ReturnsCommandError(t *testing.T) {
	r := &Runner{
		cfg: config.Config{},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			return fmt.Errorf("boom")
		},
	}

	_, _, err := r.RestartService(context.Background(), "api")
	if err == nil {
		t.Fatal("RestartService() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "docker compose restart") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunnerResolveTargetServices_AllServices(t *testing.T) {
	r := &Runner{
		cfg: config.Config{},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"worker": map[string]any{},
					"api":    map[string]any{},
				},
			}, nil
		},
	}

	got, err := r.ResolveTargetServices(context.Background())
	if err != nil {
		t.Fatalf("ResolveTargetServices() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"api", "worker"}) {
		t.Fatalf("ResolveTargetServices() = %#v", got)
	}
}

func TestRunnerResolveTargetServices_RespectWhitelist(t *testing.T) {
	r := &Runner{
		cfg: config.Config{
			AllowedServices: map[string]struct{}{
				"worker": {},
			},
		},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"worker": map[string]any{},
					"api":    map[string]any{},
				},
			}, nil
		},
	}

	got, err := r.ResolveTargetServices(context.Background())
	if err != nil {
		t.Fatalf("ResolveTargetServices() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"worker"}) {
		t.Fatalf("ResolveTargetServices() = %#v", got)
	}
}

func TestRunnerResolveTargetServices_NoAllowedServices(t *testing.T) {
	r := &Runner{
		cfg: config.Config{
			AllowedServices: map[string]struct{}{
				"cron": {},
			},
		},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"worker": map[string]any{},
					"api":    map[string]any{},
				},
			}, nil
		},
	}

	_, err := r.ResolveTargetServices(context.Background())
	if err != ErrNoAllowedServices {
		t.Fatalf("ResolveTargetServices() error = %v, want %v", err, ErrNoAllowedServices)
	}
}

func TestRunnerUpdateServices_ResolveWhenServicesEmpty(t *testing.T) {
	var gotArgs [][]string
	r := &Runner{
		cfg: config.Config{
			AllowedServices: map[string]struct{}{
				"api": {},
			},
		},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"services": map[string]any{
					"api":    map[string]any{"image": "repo/api:latest"},
					"worker": map[string]any{"image": "repo/worker:latest"},
				},
			}, nil
		},
		localImageIDFn: func(ctx context.Context, imageRef string) (string, error) {
			return "sha256:same", nil
		},
		runFn: func(ctx context.Context, args []string, logSink io.Writer) error {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			_, _ = io.WriteString(logSink, "pull output\n")
			return nil
		},
	}

	msg, _, err := r.UpdateServices(context.Background(), nil)
	if err != nil {
		t.Fatalf("UpdateServices() error = %v", err)
	}
	if msg != "所有服务 pull 后镜像 ID 均未变化，已跳过重启" {
		t.Fatalf("unexpected message: %q", msg)
	}
	wantArgs := [][]string{
		{"compose", "pull", "api"},
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("run args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestCappedBuffer_ConcurrentWrite(t *testing.T) {
	var (
		buf cappedBuffer
		wg  sync.WaitGroup
	)

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := buf.Write([]byte("line\n")); err != nil {
					t.Errorf("Write() error = %v", err)
					return
				}
				_ = buf.String()
			}
		}()
	}

	wg.Wait()

	if got := buf.String(); got == "" {
		t.Fatal("并发写入后日志不应为空")
	}
}
