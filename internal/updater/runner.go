// Package updater 负责调用宿主机上的 docker compose 完成镜像拉取与可选的容器更新。
package updater

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/biu7/updater/internal/config"
)

// Runner 封装对 docker compose 的调用。
type Runner struct {
	cfg config.Config

	runFn               func(ctx context.Context, args []string, logSink io.Writer) error
	composeConfigRootFn func(ctx context.Context) (map[string]any, error)
	localImageIDFn      func(ctx context.Context, imageRef string) (string, error)
}

// NewRunner 创建执行器。
func NewRunner(cfg config.Config) *Runner {
	return &Runner{cfg: cfg}
}

// composeBaseArgs 生成 docker compose 的固定前缀参数（含可选 -f）。
func (r *Runner) composeBaseArgs() []string {
	args := []string{"compose"}
	for _, f := range r.cfg.ComposeFiles {
		args = append(args, "-f", f)
	}
	return args
}

// UpdateService 先 pull，再仅在“明确确认已拿到新镜像”时执行 up -d。
// 优先根据 pull 前后「同一 image 引用」的本地镜像 ID 是否变化判断；
// 若服务没有 image 字段，则仅在 pull 输出明确表明“无需拉取”时跳过，否则也保守跳过，
// 避免在证据不足时做有副作用的重启。Compose 表格里反复出现 “Pulled” 不代表拉取了新层。
func (r *Runner) UpdateService(ctx context.Context, service string) (message string, log string, err error) {
	var buf cappedBuffer
	w := io.MultiWriter(&buf)

	var imageRef string
	var haveImageRef bool
	if root, cfgErr := r.composeConfigRoot(ctx); cfgErr != nil {
		fmt.Fprintf(w, "[updater] 警告: 无法读取 compose config（无法确认镜像引用，将采用保守策略避免误重启）: %v\n", cfgErr)
	} else if ref, ok := imageRefFromComposeConfig(root, service); ok {
		imageRef = ref
		haveImageRef = true
		fmt.Fprintf(w, "[updater] 从 compose 解析到镜像引用: %s\n", imageRef)
	} else {
		fmt.Fprintf(w, "[updater] 提示: 服务 %q 无可用 image 字符串（例如仅 build），将仅在 pull 输出可明确判定时重启\n", service)
	}

	var idBefore string
	if haveImageRef {
		if id, err := r.localImageID(ctx, imageRef); err == nil && id != "" {
			idBefore = id
			fmt.Fprintf(w, "[updater] pull 前本地镜像 ID: %s\n", idBefore)
		} else {
			fmt.Fprintf(w, "[updater] pull 前本地尚无该镜像或无法读取 ID（将视为可能更新）\n")
		}
	}

	pullArgs := append(r.composeBaseArgs(), "pull", service)
	if e := r.run(ctx, pullArgs, w); e != nil {
		return "", buf.String(), fmt.Errorf("docker compose pull: %w", e)
	}

	skipUp := false
	skipReason := ""
	if haveImageRef {
		idAfter, errAfter := r.localImageID(ctx, imageRef)
		if errAfter != nil || idAfter == "" {
			fmt.Fprintf(w, "[updater] 警告: pull 后仍无法读取镜像 ID，无法确认是否更新，将跳过重启: %v\n", errAfter)
			skipUp = true
			skipReason = "无法确认 pull 后镜像是否已更新，已跳过重启"
		} else {
			fmt.Fprintf(w, "[updater] pull 后本地镜像 ID: %s\n", idAfter)
			if idBefore != "" && idBefore == idAfter {
				skipUp = true
				skipReason = "pull 后镜像 ID 未变化，已跳过重启"
			}
		}
	}

	combined := buf.String()
	if skipUp {
		return skipReason, combined, nil
	}
	// 无 image 字段时无法对比 ID；仅在输出能明确判定时给出“无更新”结论，否则保守跳过。
	if !haveImageRef {
		if PullIndicatesNoNewImage(combined) {
			return "pull 未发现可更新镜像（输出判定），已跳过重启", combined, nil
		}
		return "无法确认是否已拉取到新镜像，已跳过重启", combined, nil
	}

	buf2 := cappedBuffer{maxBytes: buf.maxBytes}
	// 将 pull 日志保留，并追加 up 输出
	_, _ = buf2.Write([]byte(combined))
	w2 := io.MultiWriter(&buf2)

	upArgs := append(r.composeBaseArgs(), "up", "-d", service)
	if e := r.run(ctx, upArgs, w2); e != nil {
		return "", buf2.String(), fmt.Errorf("docker compose up -d: %w", e)
	}
	return "更新已完成（已执行 pull 与 up -d）", buf2.String(), nil
}

func (r *Runner) run(ctx context.Context, args []string, logSink io.Writer) error {
	if r.runFn != nil {
		return r.runFn(ctx, args, logSink)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = r.cfg.ComposeProjectDir
	var stderr bytes.Buffer
	cmd.Stdout = logSink
	cmd.Stderr = io.MultiWriter(logSink, &stderr)
	err := cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}

func (r *Runner) composeConfigRoot(ctx context.Context) (map[string]any, error) {
	if r.composeConfigRootFn != nil {
		return r.composeConfigRootFn(ctx)
	}
	return r.composeConfigRootExec(ctx)
}

func (r *Runner) localImageID(ctx context.Context, imageRef string) (string, error) {
	if r.localImageIDFn != nil {
		return r.localImageIDFn(ctx, imageRef)
	}
	return r.localImageIDExec(ctx, imageRef)
}

// cappedBuffer 限制总长度，仅保留末尾一段，避免内存无限增长。
type cappedBuffer struct {
	mu       sync.Mutex
	b        []byte
	maxBytes int
}

const defaultLogCap = 64 << 10 // 64KiB

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxBytes == 0 {
		c.maxBytes = defaultLogCap
	}
	c.b = append(c.b, p...)
	if len(c.b) > c.maxBytes {
		c.b = c.b[len(c.b)-c.maxBytes:]
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.b)
}
