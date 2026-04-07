package updater

import (
	"regexp"
	"strings"
)

var (
	reSkippedUpToDate = regexp.MustCompile(`(?i)skipped[^\n]*up\s+to\s+date`)
	reImageUpToDate   = regexp.MustCompile(`(?i)image\s+is\s+up\s+to\s+date`)
	reNoPullableImage = regexp.MustCompile(`(?i)(no\s+image\s+to\s+be\s+pulled|must\s+be\s+built\s+from\s+source|can\s+be\s+built\s+from\s+source)`)
	// 明确出现新层拉取或提示已下载较新镜像时，视为有新镜像。
	reDownloadHint = regexp.MustCompile(`(?i)(downloaded\s+newer\s+image|pulling\s+fs\s+layer|pull\s+complete|downloading|extracting)`)
)

// PullIndicatesNoNewImage 根据 docker compose pull 的合并输出判断是否未拉取到新镜像。
// Compose V2 常见为 “Skipped - Image is up to date”；旧版或不同驱动输出可能略有差异。
func PullIndicatesNoNewImage(combinedOutput string) bool {
	s := strings.TrimSpace(combinedOutput)
	if s == "" {
		return false
	}
	if reDownloadHint.MatchString(s) {
		return false
	}
	return reSkippedUpToDate.MatchString(s) || reImageUpToDate.MatchString(s) || reNoPullableImage.MatchString(s)
}
