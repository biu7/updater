// Package config 从环境变量加载 updater 运行参数。
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 从环境变量加载的运行时配置。
type Config struct {
	Port                    string
	ComposeProjectDir       string
	ComposeProjectDirectory string   // 可选，对应 docker compose --project-directory
	ComposeFiles            []string // 可选，对应 COMPOSE_FILE，支持逗号分隔多个文件
	AllowedServices         map[string]struct{}
	UpdateTimeout           time.Duration
}

// Load 读取环境变量并返回配置；缺少必填项时返回错误。
func Load() (Config, error) {
	return LoadFromArgs(os.Args[1:])
}

// LoadFromArgs 读取环境变量与命令行参数并返回配置。
func LoadFromArgs(args []string) (Config, error) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dir := os.Getenv("COMPOSE_PROJECT_DIR")
	if dir == "" {
		dir = "/workspace/compose-project"
	}

	composeProjectDirectory := strings.TrimSpace(os.Getenv("COMPOSE_PROJECT_DIRECTORY"))
	flagSet := flag.NewFlagSet("updater", flag.ContinueOnError)
	projectDirectoryFlag := flagSet.String("project-directory", "", "docker compose 的 --project-directory 参数")
	flagSet.SetOutput(io.Discard)
	if err := flagSet.Parse(args); err != nil {
		return Config{}, err
	}
	if v := strings.TrimSpace(*projectDirectoryFlag); v != "" {
		composeProjectDirectory = v
	}

	var files []string
	if cf := strings.TrimSpace(os.Getenv("COMPOSE_FILE")); cf != "" {
		for _, p := range strings.Split(cf, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				files = append(files, p)
			}
		}
	}

	allowed := make(map[string]struct{})
	if raw := strings.TrimSpace(os.Getenv("ALLOWED_SERVICES")); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				allowed[s] = struct{}{}
			}
		}
	}

	timeout := 10 * time.Minute
	if v := strings.TrimSpace(os.Getenv("UPDATE_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("UPDATE_TIMEOUT 无效: %w", err)
		}
		timeout = d
	}

	return Config{
		Port:                    port,
		ComposeProjectDir:       dir,
		ComposeProjectDirectory: composeProjectDirectory,
		ComposeFiles:            files,
		AllowedServices:         allowed,
		UpdateTimeout:           timeout,
	}, nil
}

// IsServiceAllowed 若未配置白名单则全部允许；否则仅允许白名单内服务。
func (c Config) IsServiceAllowed(service string) bool {
	if len(c.AllowedServices) == 0 {
		return true
	}
	_, ok := c.AllowedServices[service]
	return ok
}

// Addr 返回 HTTP 监听地址。
func (c Config) Addr() string {
	// 已含 host:port 时原样使用；否则视为仅端口，监听所有接口。
	if strings.Contains(c.Port, ":") {
		return c.Port
	}
	return ":" + c.Port
}

// ParsePortInt 用于日志等场景解析端口号。
func (c Config) ParsePortInt() int {
	p := strings.TrimPrefix(c.Addr(), ":")
	if i := strings.LastIndex(p, ":"); i >= 0 {
		p = p[i+1:]
	}
	n, _ := strconv.Atoi(p)
	return n
}
