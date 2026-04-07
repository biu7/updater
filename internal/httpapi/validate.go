package httpapi

import (
	"fmt"
	"regexp"
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
