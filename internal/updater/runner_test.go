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

func TestRunnerComposeBaseArgs_WithProjectDirectory(t *testing.T) {
	r := &Runner{
		cfg: config.Config{
			ComposeProjectDirectory: "/host/project",
			ComposeFiles:            []string{"docker-compose.yml"},
		},
	}

	got := r.composeBaseArgs()
	want := []string{"compose", "--project-directory", "/host/project", "-f", "docker-compose.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("composeBaseArgs() = %#v, want %#v", got, want)
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
