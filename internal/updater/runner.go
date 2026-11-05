// Package updater 负责调用宿主机上的 docker compose 完成镜像拉取与可选的容器更新。
package updater

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/biu7/updater/internal/config"
)

// Runner 封装对 docker compose 的调用。
type Runner struct {
	cfg config.Config

	runFn                    func(ctx context.Context, args []string, logSink io.Writer) error
	composeConfigRootFn      func(ctx context.Context) (map[string]any, error)
	localImageIDFn           func(ctx context.Context, imageRef string) (string, error)
	runningServiceImageIDsFn func(ctx context.Context, service string) ([]string, error)
	// outputFn 替换 docker 子命令执行（测试用）；为 nil 时执行真实 docker。
	outputFn func(ctx context.Context, args []string) ([]byte, error)
}

var (
	// ErrNoComposeServices 表示 compose 配置中未解析到任何服务。
	ErrNoComposeServices = errors.New("compose 中未找到任何服务")
	// ErrNoAllowedServices 表示配置了白名单，但 compose 中没有命中任何允许服务。
	ErrNoAllowedServices = errors.New("compose 中未找到允许更新的服务")
)

// NewRunner 创建执行器。
func NewRunner(cfg config.Config) *Runner {
	return &Runner{cfg: cfg}
}

// composeBaseArgs 生成 docker compose 的固定前缀参数（含可选 -f）。
func (r *Runner) composeBaseArgs() []string {
	args := []string{"compose"}
	if r.cfg.ComposeProjectDirectory != "" {
		args = append(args, "--project-directory", r.cfg.ComposeProjectDirectory)
	}
	for _, f := range r.cfg.ComposeFiles {
		args = append(args, "-f", f)
	}
	return args
}

// ResolveTargetServices 解析本次操作实际涉及的服务列表。
func (r *Runner) ResolveTargetServices(ctx context.Context) ([]string, error) {
	root, err := r.composeConfigRoot(ctx)
	if err != nil {
		return nil, err
	}
	allServices := serviceNamesFromComposeConfig(root)
	if len(allServices) == 0 {
		return nil, ErrNoComposeServices
	}
	if len(r.cfg.AllowedServices) == 0 {
		return allServices, nil
	}
	targets := make([]string, 0, len(allServices))
	for _, service := range allServices {
		if r.cfg.IsServiceAllowed(service) {
			targets = append(targets, service)
		}
	}
	if len(targets) == 0 {
		return nil, ErrNoAllowedServices
	}
	return targets, nil
}

// Result 是 pull/update 任务的结构化结果。
// NeedsUp：判定需要 up 的服务（无运行容器，或任一容器镜像 ≠ 本地 tag 当前镜像 ID）；
// Upped：本次实际执行 up -d --no-deps 的服务；Uncertain：无法判定、因而未 up 的服务。
type Result struct {
	Message   string
	Log       string
	NeedsUp   []string
	Upped     []string
	Uncertain []string
}

// ErrInvalidServices 表示请求的服务不在 compose 服务与 ALLOWED_SERVICES 的交集中。
var ErrInvalidServices = errors.New("请求的服务不在允许范围内")

// ValidateServices 校验 requested ⊆ compose 服务 ∩ ALLOWED_SERVICES；不满足时返回包装了 ErrInvalidServices 的错误。
func (r *Runner) ValidateServices(ctx context.Context, requested []string) error {
	targets, err := r.ResolveTargetServices(ctx)
	if err != nil && !errors.Is(err, ErrNoAllowedServices) && !errors.Is(err, ErrNoComposeServices) {
		return err
	}
	var invalid []string
	for _, s := range requested {
		if !slices.Contains(targets, s) {
			invalid = append(invalid, s)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidServices, strings.Join(invalid, ","))
	}
	return nil
}

// PullServices 仅对指定服务执行 compose pull，并返回 needs_up / uncertain 判定，不执行 up。
func (r *Runner) PullServices(ctx context.Context, services []string) (Result, error) {
	var buf cappedBuffer
	fmt.Fprintf(&buf, "[updater] 本次拉取目标服务: %s\n", strings.Join(services, ","))
	res, err := r.pullAndEvaluate(ctx, services, true, &buf)
	res.Log = buf.String()
	if err != nil {
		return res, err
	}
	switch {
	case len(res.NeedsUp) == 0 && len(res.Uncertain) == 0:
		res.Message = "拉取已完成，所有服务运行容器已使用本地最新镜像"
	default:
		res.Message = fmt.Sprintf("拉取已完成（需要应用镜像的服务：%s；判定不确定的服务：%s）",
			joinOrNone(res.NeedsUp), joinOrNone(res.Uncertain))
	}
	return res, nil
}

// UpdateServices 按需 pull（pull=true）后，只对 needs_up 服务执行 compose up -d --no-deps。
// 判据不确定的服务进入 Uncertain，不执行 up。
func (r *Runner) UpdateServices(ctx context.Context, services []string, pull bool) (Result, error) {
	var buf cappedBuffer
	fmt.Fprintf(&buf, "[updater] 本次更新目标服务: %s（pull=%t）\n", strings.Join(services, ","), pull)
	res, err := r.pullAndEvaluate(ctx, services, pull, &buf)
	if err != nil {
		res.Log = buf.String()
		return res, err
	}
	if len(res.NeedsUp) == 0 {
		res.Log = buf.String()
		if len(res.Uncertain) > 0 {
			res.Message = fmt.Sprintf("无法确认部分服务是否需要更新，已跳过重启（不确定：%s）", strings.Join(res.Uncertain, ","))
		} else {
			res.Message = "所有服务运行容器已使用本地最新镜像（镜像未变化），已跳过重启"
		}
		return res, nil
	}

	upArgs := append(r.composeBaseArgs(), "up", "-d", "--no-deps")
	upArgs = append(upArgs, res.NeedsUp...)
	if e := r.run(ctx, upArgs, &buf); e != nil {
		res.Log = buf.String()
		return res, fmt.Errorf("docker compose up -d: %w", e)
	}
	res.Upped = append([]string(nil), res.NeedsUp...)
	res.Log = buf.String()
	res.Message = fmt.Sprintf("更新已完成（已执行 up -d --no-deps 的服务：%s）", strings.Join(res.Upped, ","))
	if len(res.Uncertain) > 0 {
		res.Message += fmt.Sprintf("；无法确认的服务未重启：%s", strings.Join(res.Uncertain, ","))
	}
	return res, nil
}

// pullAndEvaluate 可选执行 pull，然后按 needs_up 判据逐服务判定。
// pull 前后的本地镜像 ID 对比只写日志，不参与判定。
func (r *Runner) pullAndEvaluate(ctx context.Context, services []string, pull bool, w io.Writer) (Result, error) {
	res := Result{NeedsUp: []string{}, Upped: []string{}, Uncertain: []string{}}

	refs := make(map[string]string, len(services))
	if root, cfgErr := r.composeConfigRoot(ctx); cfgErr != nil {
		fmt.Fprintf(w, "[updater] 警告: 无法读取 compose config，所有服务视为判定不确定: %v\n", cfgErr)
	} else {
		for _, service := range services {
			if ref, ok := imageRefFromComposeConfig(root, service); ok {
				refs[service] = ref
				fmt.Fprintf(w, "[updater] 服务 %q 从 compose 解析到镜像引用: %s\n", service, ref)
			} else {
				fmt.Fprintf(w, "[updater] 服务 %q 无可用 image 字符串（例如仅 build），判定不确定\n", service)
			}
		}
	}

	before := make(map[string]string, len(refs))
	if pull {
		for service, ref := range refs {
			if id, err := r.localImageID(ctx, ref); err == nil && id != "" {
				before[service] = id
			}
		}
		pullArgs := append(r.composeBaseArgs(), "pull")
		pullArgs = append(pullArgs, services...)
		if e := r.run(ctx, pullArgs, w); e != nil {
			return res, fmt.Errorf("docker compose pull: %w", e)
		}
	}

	for _, service := range services {
		ref, ok := refs[service]
		if !ok {
			res.Uncertain = append(res.Uncertain, service)
			continue
		}
		localID, err := r.localImageID(ctx, ref)
		if err != nil || localID == "" {
			fmt.Fprintf(w, "[updater] 警告: 服务 %q 无法读取本地镜像 %s 的 ID，判定不确定: %v\n", service, ref, err)
			res.Uncertain = append(res.Uncertain, service)
			continue
		}
		if pull {
			switch prev := before[service]; {
			case prev == "":
				fmt.Fprintf(w, "[updater] 服务 %q pull 后本地镜像 ID: %s（pull 前无法读取）\n", service, localID)
			case prev != localID:
				fmt.Fprintf(w, "[updater] 服务 %q pull 后本地镜像 ID 变化: %s -> %s\n", service, prev, localID)
			default:
				fmt.Fprintf(w, "[updater] 服务 %q pull 后本地镜像 ID 未变: %s\n", service, localID)
			}
		} else {
			fmt.Fprintf(w, "[updater] 服务 %q 本地镜像 ID: %s\n", service, localID)
		}

		runningIDs, err := r.runningServiceImageIDs(ctx, service)
		if err != nil {
			fmt.Fprintf(w, "[updater] 警告: 服务 %q 无法读取运行容器镜像 ID，判定不确定: %v\n", service, err)
			res.Uncertain = append(res.Uncertain, service)
			continue
		}
		if len(runningIDs) == 0 {
			fmt.Fprintf(w, "[updater] 服务 %q 当前没有运行中的容器，需要 up\n", service)
			res.NeedsUp = append(res.NeedsUp, service)
			continue
		}
		if slices.ContainsFunc(runningIDs, func(id string) bool { return id != localID }) {
			fmt.Fprintf(w, "[updater] 服务 %q 运行容器镜像 %s 与本地最新镜像不一致，需要 up\n", service, strings.Join(runningIDs, ","))
			res.NeedsUp = append(res.NeedsUp, service)
			continue
		}
		fmt.Fprintf(w, "[updater] 服务 %q 运行容器已使用本地最新镜像，无需 up\n", service)
	}
	return res, nil
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "无"
	}
	return strings.Join(s, ",")
}

// RestartServices 直接执行 docker compose restart；services 为空时会自动解析目标服务列表。
func (r *Runner) RestartServices(ctx context.Context, services []string) (message string, log string, err error) {
	var buf cappedBuffer
	w := io.MultiWriter(&buf)

	if len(services) == 0 {
		services, err = r.ResolveTargetServices(ctx)
		if err != nil {
			return "", "", err
		}
	}
	fmt.Fprintf(w, "[updater] 本次重启目标服务: %s\n", strings.Join(services, ","))

	restartArgs := append(r.composeBaseArgs(), "restart")
	restartArgs = append(restartArgs, services...)
	if e := r.run(ctx, restartArgs, w); e != nil {
		return "", buf.String(), fmt.Errorf("docker compose restart: %w", e)
	}
	return "重启已完成（已执行 restart）", buf.String(), nil
}

// RestartService 兼容单服务调用。
func (r *Runner) RestartService(ctx context.Context, service string) (message string, log string, err error) {
	return r.RestartServices(ctx, []string{service})
}

func (r *Runner) run(ctx context.Context, args []string, logSink io.Writer) error {
	if r.runFn != nil {
		return r.runFn(ctx, args, logSink)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = r.cfg.ComposeProjectDir
	cmd.WaitDelay = cmdWaitDelay
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

func (r *Runner) runningServiceImageIDs(ctx context.Context, service string) ([]string, error) {
	if r.runningServiceImageIDsFn != nil {
		return r.runningServiceImageIDsFn(ctx, service)
	}
	return r.runningServiceImageIDsExec(ctx, service)
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
