package updater

// 本文件实现只读的版本检查：对比运行中容器镜像与远程 registry 的 index/manifest digest。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// 版本来源白名单：只读取这两个字段，永不返回镜像 Env 全文。
const (
	labelImageVersion = "org.opencontainers.image.version"
	envIBGatewayVer   = "IB_GATEWAY_VERSION"
)

// 检查结果的稳定错误码。
const (
	CheckErrComposeConfig       = "compose_config_failed"
	CheckErrImageRefMissing     = "image_ref_missing"
	CheckErrNotRunning          = "not_running"
	CheckErrLocalInspect        = "local_inspect_failed"
	CheckErrRemoteTimeout       = "remote_timeout"
	CheckErrRemoteAuth          = "remote_auth_failed"
	CheckErrRemoteNotFound      = "remote_not_found"
	CheckErrRemoteUnavailable   = "remote_unavailable"
	CheckErrRemoteInvalidOutput = "remote_invalid_response"
)

// CheckResult 是单个服务的检查结果。UpdateAvailable 为 nil 表示无法判定（见 ErrorCode）。
type CheckResult struct {
	Service         string `json:"service"`
	Image           string `json:"image"`
	CurrentVersion  string `json:"current_version"`
	RemoteVersion   string `json:"remote_version"`
	UpdateAvailable *bool  `json:"update_available"`
	ErrorCode       string `json:"error_code,omitempty"`
}

// imageConfig 是镜像配置中只关心的部分。
type imageConfig struct {
	Labels map[string]string `json:"Labels"`
	Env    []string          `json:"Env"`
}

// versionFromConfig 优先取 env IB_GATEWAY_VERSION，否则取 OCI version label。
func versionFromConfig(c imageConfig) string {
	for _, kv := range c.Env {
		if v, ok := strings.CutPrefix(kv, envIBGatewayVer+"="); ok && v != "" {
			return v
		}
	}
	return c.Labels[labelImageVersion]
}

// CheckServices 并发检查各服务；不占用任务锁，调用方负责整体期限。
func (r *Runner) CheckServices(ctx context.Context, services []string) []CheckResult {
	results := make([]CheckResult, len(services))
	root, cfgErr := r.composeConfigRoot(ctx)
	var wg sync.WaitGroup
	for i, service := range services {
		results[i] = CheckResult{Service: service}
		if cfgErr != nil {
			results[i].ErrorCode = CheckErrComposeConfig
			continue
		}
		ref, ok := imageRefFromComposeConfig(root, service)
		if !ok {
			results[i].ErrorCode = CheckErrImageRefMissing
			continue
		}
		results[i].Image = ref
		wg.Add(1)
		go func(res *CheckResult) {
			defer wg.Done()
			r.checkService(ctx, res)
		}(&results[i])
	}
	wg.Wait()
	return results
}

func (r *Runner) checkService(ctx context.Context, res *CheckResult) {
	var (
		wg                      sync.WaitGroup
		remoteDigest, remoteErr string
		local                   []localImage
		localErr                string
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		var ver string
		remoteDigest, ver, remoteErr = r.remoteImage(ctx, res.Image)
		res.RemoteVersion = ver
	}()
	go func() {
		defer wg.Done()
		local, localErr = r.runningImages(ctx, res.Service)
	}()
	wg.Wait()

	if len(local) > 0 {
		res.CurrentVersion = local[0].version
	}
	switch {
	case localErr != "":
		res.ErrorCode = localErr
	case remoteErr != "":
		res.ErrorCode = remoteErr
	default:
		available := slices.ContainsFunc(local, func(img localImage) bool { return !img.hasDigest(remoteDigest) })
		res.UpdateAvailable = &available
	}
}

// localImage 是运行中容器所用镜像的必要信息。
type localImage struct {
	id          string
	repoDigests []string
	version     string
}

// hasDigest 判断本地镜像是否就是远程 digest 对应的镜像：
// 经典 store 看 RepoDigests（repo@digest）；containerd image store 下本地 ID 即 index digest。
func (img localImage) hasDigest(digest string) bool {
	if img.id == digest {
		return true
	}
	for _, rd := range img.repoDigests {
		if _, d, ok := strings.Cut(rd, "@"); ok && d == digest {
			return true
		}
	}
	return false
}

// runningImages 返回服务运行中容器所用的（去重）镜像；失败时返回稳定错误码。
func (r *Runner) runningImages(ctx context.Context, service string) ([]localImage, string) {
	ids, err := r.runningServiceImageIDs(ctx, service)
	if err != nil {
		return nil, CheckErrLocalInspect
	}
	if len(ids) == 0 {
		return nil, CheckErrNotRunning
	}
	out, err := r.output(ctx, append([]string{"image", "inspect"}, ids...))
	if err != nil {
		return nil, CheckErrLocalInspect
	}
	var raw []struct {
		ID          string      `json:"Id"`
		RepoDigests []string    `json:"RepoDigests"`
		Config      imageConfig `json:"Config"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || len(raw) != len(ids) {
		return nil, CheckErrLocalInspect
	}
	images := make([]localImage, 0, len(raw))
	for _, item := range raw {
		images = append(images, localImage{id: item.ID, repoDigests: item.RepoDigests, version: versionFromConfig(item.Config)})
	}
	return images, ""
}

// remoteImage 通过 buildx imagetools 读取远程 index/manifest digest 与版本。
func (r *Runner) remoteImage(ctx context.Context, ref string) (digest, version, errCode string) {
	out, err := r.output(ctx, []string{"buildx", "imagetools", "inspect", "--format", "{{json .}}", ref})
	if err != nil {
		return "", "", classifyRemoteError(ctx, err)
	}
	var raw struct {
		Manifest struct {
			Digest string `json:"digest"`
		} `json:"manifest"`
		Image json.RawMessage `json:"image"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || !strings.HasPrefix(raw.Manifest.Digest, "sha256:") {
		return "", "", CheckErrRemoteInvalidOutput
	}
	cfg, _ := remoteImageConfig(raw.Image)
	return raw.Manifest.Digest, versionFromConfig(cfg), ""
}

type remoteConfigDoc struct {
	Config *imageConfig `json:"config"`
}

// remoteImageConfig 解析 imagetools 的 image 字段：单平台为配置对象，多平台为 "os/arch" → 配置 的映射。
func remoteImageConfig(raw json.RawMessage) (imageConfig, bool) {
	if len(raw) == 0 {
		return imageConfig{}, false
	}
	var single remoteConfigDoc
	if err := json.Unmarshal(raw, &single); err == nil && single.Config != nil {
		return *single.Config, true
	}
	var multi map[string]remoteConfigDoc
	if err := json.Unmarshal(raw, &multi); err != nil {
		return imageConfig{}, false
	}
	if doc, ok := multi[runtime.GOOS+"/"+runtime.GOARCH]; ok && doc.Config != nil {
		return *doc.Config, true
	}
	keys := make([]string, 0, len(multi))
	for k := range multi {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if multi[k].Config != nil {
			return *multi[k].Config, true
		}
	}
	return imageConfig{}, false
}

func classifyRemoteError(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return CheckErrRemoteTimeout
	}
	msg := strings.ToLower(fmt.Sprint(err))
	switch {
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "authorize"),
		strings.Contains(msg, "denied"), strings.Contains(msg, "401"), strings.Contains(msg, "403"):
		return CheckErrRemoteAuth
	case strings.Contains(msg, "not found"), strings.Contains(msg, "manifest unknown"):
		return CheckErrRemoteNotFound
	default:
		return CheckErrRemoteUnavailable
	}
}
