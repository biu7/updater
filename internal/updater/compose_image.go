package updater

// 本文件通过 compose config 解析镜像引用，并用 docker image inspect 对比 pull 前后镜像 ID。

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// composeConfigRootExec 执行 docker compose config --format json 并解析为通用 map。
func (r *Runner) composeConfigRootExec(ctx context.Context) (map[string]any, error) {
	args := append(r.composeBaseArgs(), "config", "--format", "json")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = r.cfg.ComposeProjectDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose config: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("解析 compose config JSON: %w", err)
	}
	return root, nil
}

// imageRefFromComposeConfig 从已解析的 config 中取服务的 image 字符串（build-only 无 image 时返回 false）。
func imageRefFromComposeConfig(root map[string]any, service string) (ref string, ok bool) {
	services, _ := root["services"].(map[string]any)
	if services == nil {
		return "", false
	}
	svc, _ := services[service].(map[string]any)
	if svc == nil {
		return "", false
	}
	img, ok := svc["image"].(string)
	if !ok {
		return "", false
	}
	img = strings.TrimSpace(img)
	if img == "" {
		return "", false
	}
	return img, true
}

// localImageIDExec 返回本地已存在镜像的 ID（docker image inspect），不存在或失败时返回 ("", error)。
func (r *Runner) localImageIDExec(ctx context.Context, imageRef string) (string, error) {
	if imageRef == "" {
		return "", fmt.Errorf("镜像引用为空")
	}
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", imageRef, "-f", "{{.Id}}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
