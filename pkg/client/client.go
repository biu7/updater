package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config 客户端配置。
type Config struct {
	// BaseURL 服务根地址，例如 http://127.0.0.1:8080（无尾部斜杠亦可）。
	BaseURL string
	// HTTPClient 可选；为零值时使用默认客户端（10s 超时）。
	HTTPClient *http.Client
}

// Client 调用 updater HTTP API 的客户端。
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New 根据配置构造客户端。
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("client: BaseURL 不能为空")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: base, httpClient: hc}, nil
}

// NewWithBaseURL 使用默认 HTTP 客户端创建客户端，等价于 New(Config{BaseURL: u})。
func NewWithBaseURL(baseURL string) (*Client, error) {
	return New(Config{BaseURL: baseURL})
}

func (c *Client) url(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.baseURL + path
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Detail  string          `json:"detail,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (httpStatus int, env apiEnvelope, rawBody []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return 0, apiEnvelope{}, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, apiEnvelope{}, nil, err
	}
	defer resp.Body.Close()
	rawBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, apiEnvelope{}, nil, err
	}
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return resp.StatusCode, apiEnvelope{}, rawBody, fmt.Errorf("client: 响应不是合法 JSON: %w", err)
	}
	return resp.StatusCode, env, rawBody, nil
}

// Health 调用 GET /health。
func (c *Client) Health(ctx context.Context) (*HealthResult, error) {
	status, env, _, err := c.doRequest(ctx, http.MethodGet, "/health", nil, "")
	if err != nil {
		return nil, err
	}
	out := &HealthResult{
		HTTPStatus: status,
		Code:       env.Code,
		Message:    env.Message,
		Detail:     env.Detail,
	}
	if len(env.Data) > 0 {
		var d struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(env.Data, &d); err == nil {
			out.Status = d.Status
		}
	}
	return out, nil
}

// Update 调用 POST /update，异步创建更新任务。
func (c *Client) Update(ctx context.Context, service string) (*CreateUpdateResult, error) {
	payload, err := json.Marshal(map[string]string{"service": service})
	if err != nil {
		return nil, err
	}
	status, env, _, err := c.doRequest(ctx, http.MethodPost, "/update", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	out := &CreateUpdateResult{
		HTTPStatus: status,
		Code:       env.Code,
		Message:    env.Message,
		Detail:     env.Detail,
	}
	if len(env.Data) == 0 {
		return out, nil
	}
	// 成功创建
	var created struct {
		JobID   string `json:"job_id"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(env.Data, &created); err == nil && created.JobID != "" {
		out.JobID = created.JobID
		out.Service = created.Service
		return out, nil
	}
	// 409 冲突
	var conflict struct {
		ExistingJobID string    `json:"existing_job_id"`
		Service       string    `json:"service"`
		Status        JobStatus `json:"status"`
	}
	if err := json.Unmarshal(env.Data, &conflict); err == nil && conflict.ExistingJobID != "" {
		out.ExistingJobID = conflict.ExistingJobID
		out.ExistingService = conflict.Service
		out.ExistingStatus = conflict.Status
	}
	return out, nil
}

// GetJob 调用 GET /jobs/:id。
func (c *Client) GetJob(ctx context.Context, jobID string) (*JobResult, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("client: jobID 不能为空")
	}
	path := "/jobs/" + jobID
	status, env, _, err := c.doRequest(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	out := &JobResult{
		HTTPStatus: status,
		Code:       env.Code,
		Message:    env.Message,
		Detail:     env.Detail,
	}
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &out.Job)
	}
	return out, nil
}
