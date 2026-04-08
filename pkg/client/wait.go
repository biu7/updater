package client

import (
	"context"
	"time"
)

// WaitJob 轮询 GET /jobs/:id，直到任务进入终态（succeeded / skipped / failed）或 ctx 取消。
// pollEvery 为零或负数时使用 500ms。
func (c *Client) WaitJob(ctx context.Context, jobID string, pollEvery time.Duration) (*JobResult, error) {
	if pollEvery <= 0 {
		pollEvery = 500 * time.Millisecond
	}
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()

	for {
		res, err := c.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if res.Done() {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-ticker.C:
		}
	}
}
