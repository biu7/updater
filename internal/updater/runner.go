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

// UpdateServices 先 pull，再仅在“本地最新镜像尚未被当前容器实际使用”时执行 up -d。
// 这既覆盖新镜像刚被拉取的场景，也覆盖上次 up -d 失败导致容器仍停留在旧镜像的场景。
func (r *Runner) UpdateServices(ctx context.Context, services []string) (message string, log string, err error) {
	var buf cappedBuffer
	w := io.MultiWriter(&buf)

	if len(services) == 0 {
		services, err = r.ResolveTargetServices(ctx)
		if err != nil {
			return "", "", err
		}
	}
	fmt.Fprintf(w, "[updater] 本次更新目标服务: %s\n", strings.Join(services, ","))

	type imageState struct {
		service  string
		imageRef string
		idBefore string
	}

	imageStates := make([]imageState, 0, len(services))
	missingImageServices := make([]string, 0)
	if root, cfgErr := r.composeConfigRoot(ctx); cfgErr != nil {
		fmt.Fprintf(w, "[updater] 警告: 无法读取 compose config（无法确认镜像引用，将采用保守策略避免误重启）: %v\n", cfgErr)
	} else {
		for _, service := range services {
			ref, ok := imageRefFromComposeConfig(root, service)
			if !ok {
				missingImageServices = append(missingImageServices, service)
				fmt.Fprintf(w, "[updater] 提示: 服务 %q 无可用 image 字符串（例如仅 build），将仅在 pull 输出可明确判定时重启\n", service)
				continue
			}
			fmt.Fprintf(w, "[updater] 服务 %q 从 compose 解析到镜像引用: %s\n", service, ref)
			imageStates = append(imageStates, imageState{
				service:  service,
				imageRef: ref,
			})
		}
	}

	for i := range imageStates {
		if id, err := r.localImageID(ctx, imageStates[i].imageRef); err == nil && id != "" {
			imageStates[i].idBefore = id
			fmt.Fprintf(w, "[updater] 服务 %q pull 前本地镜像 ID: %s\n", imageStates[i].service, id)
		} else {
			fmt.Fprintf(w, "[updater] 服务 %q pull 前本地尚无该镜像或无法读取 ID（将视为可能更新）\n", imageStates[i].service)
		}
	}

	pullArgs := append(r.composeBaseArgs(), "pull")
	pullArgs = append(pullArgs, services...)
	if e := r.run(ctx, pullArgs, w); e != nil {
		return "", buf.String(), fmt.Errorf("docker compose pull: %w", e)
	}

	servicesNeedingUp := make([]string, 0)
	uncertainServices := make([]string, 0)
	for _, state := range imageStates {
		idAfter, errAfter := r.localImageID(ctx, state.imageRef)
		if errAfter != nil || idAfter == "" {
			fmt.Fprintf(w, "[updater] 警告: 服务 %q pull 后仍无法读取镜像 ID，无法确认是否更新: %v\n", state.service, errAfter)
			uncertainServices = append(uncertainServices, state.service)
			continue
		}
		fmt.Fprintf(w, "[updater] 服务 %q pull 后本地镜像 ID: %s\n", state.service, idAfter)

		runningImageIDs, errRunning := r.runningServiceImageIDs(ctx, state.service)
		if errRunning != nil {
			fmt.Fprintf(w, "[updater] 警告: 服务 %q 无法读取当前容器镜像 ID，无法确认是否需要执行 up -d: %v\n", state.service, errRunning)
			uncertainServices = append(uncertainServices, state.service)
			continue
		}
		if len(runningImageIDs) == 0 {
			fmt.Fprintf(w, "[updater] 服务 %q 当前没有已创建的 compose 容器，需要执行 up -d 以应用本地镜像\n", state.service)
			servicesNeedingUp = append(servicesNeedingUp, state.service)
			continue
		}
		fmt.Fprintf(w, "[updater] 服务 %q 当前容器镜像 ID: %s\n", state.service, strings.Join(runningImageIDs, ","))
		if len(runningImageIDs) != 1 || runningImageIDs[0] != idAfter {
			fmt.Fprintf(w, "[updater] 服务 %q 当前容器尚未全部使用本地最新镜像，需要执行 up -d\n", state.service)
			servicesNeedingUp = append(servicesNeedingUp, state.service)
			continue
		}
		if state.idBefore == "" || state.idBefore != idAfter {
			fmt.Fprintf(w, "[updater] 服务 %q pull 后镜像已更新，且运行容器已使用该镜像\n", state.service)
		} else {
			fmt.Fprintf(w, "[updater] 服务 %q 本地镜像与运行容器镜像一致，无需执行 up -d\n", state.service)
		}
	}

	combined := buf.String()

	if len(servicesNeedingUp) == 0 {
		if len(imageStates) == 0 {
			if PullIndicatesNoNewImage(combined) {
				return "本次 pull 未发现可更新镜像（输出判定），已跳过重启", combined, nil
			}
			return "无法确认本次 pull 是否已拉取到新镜像，已跳过重启", combined, nil
		}
		if len(uncertainServices) > 0 || len(missingImageServices) > 0 {
			return "无法确认部分服务 pull 后镜像是否已更新，已跳过重启", combined, nil
		}
		return "所有服务 pull 后镜像 ID 均未变化，已跳过重启", combined, nil
	}

	buf2 := cappedBuffer{maxBytes: buf.maxBytes}
	// 将 pull 日志保留，并追加 up 输出
	_, _ = buf2.Write([]byte(combined))
	w2 := io.MultiWriter(&buf2)

	upArgs := append(r.composeBaseArgs(), "up", "-d")
	upArgs = append(upArgs, services...)
	if e := r.run(ctx, upArgs, w2); e != nil {
		return "", buf2.String(), fmt.Errorf("docker compose up -d: %w", e)
	}
	slices.Sort(servicesNeedingUp)
	return fmt.Sprintf("更新已完成（已执行 pull 与 up -d，需要应用镜像的服务：%s）", strings.Join(servicesNeedingUp, ",")), buf2.String(), nil
}

// UpdateService 兼容单服务调用。
func (r *Runner) UpdateService(ctx context.Context, service string) (message string, log string, err error) {
	return r.UpdateServices(ctx, []string{service})
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
