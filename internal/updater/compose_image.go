package updater

// 本文件通过 compose config 解析镜像引用，并用 docker image inspect 对比 pull 前后镜像 ID。

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
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

// serviceNamesFromComposeConfig 返回 compose config 中声明的全部服务名，并按字典序排序。
func serviceNamesFromComposeConfig(root map[string]any) []string {
	services, _ := root["services"].(map[string]any)
	if len(services) == 0 {
		return nil
	}
	names := make([]string, 0, len(services))
	for name := range services {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
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

// runningServiceImageIDsExec 返回 compose 服务当前容器实际使用的镜像 ID 列表。
func (r *Runner) runningServiceImageIDsExec(ctx context.Context, service string) ([]string, error) {
	if strings.TrimSpace(service) == "" {
		return nil, fmt.Errorf("服务名为空")
	}
	args := append(r.composeBaseArgs(), "ps", "-q", service)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = r.cfg.ComposeProjectDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose ps -q %s: %w", service, err)
	}
	containerIDs := splitNonEmptyLines(string(out))
	if len(containerIDs) == 0 {
		return nil, nil
	}

	inspectArgs := append([]string{"inspect", "-f", "{{.Image}}"}, containerIDs...)
	inspectCmd := exec.CommandContext(ctx, "docker", inspectArgs...)
	inspectOut, err := inspectCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect service %s containers: %w", service, err)
	}
	return normalizeImageIDs(splitNonEmptyLines(string(inspectOut))), nil
}

// splitNonEmptyLines 将多行输出转为去空白后的切片。
func splitNonEmptyLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// normalizeImageIDs 去重并排序镜像 ID，便于稳定比较。
func normalizeImageIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
