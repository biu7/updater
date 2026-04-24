package httpapi

import (
	"fmt"
	"regexp"
	"strings"
)

var reServiceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,253}$`)

// ValidateServiceName 校验 Compose 服务名，避免空值与明显非法字符。
func ValidateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("service 不能为空")
	}
	if !reServiceName.MatchString(name) {
		return fmt.Errorf("service 名称格式不合法")
	}
	return nil
}

// NormalizeServices 校验并规范化服务列表，去重后保持原有顺序。
func NormalizeServices(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("services 不能为空")
	}

	seen := make(map[string]struct{}, len(names))
	services := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if err := ValidateServiceName(trimmed); err != nil {
			return nil, err
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		services = append(services, trimmed)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("services 不能为空")
	}
	return services, nil
}
