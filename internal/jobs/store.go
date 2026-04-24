// Package jobs 维护内存中的异步更新任务及串行执行关系。
package jobs

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrConflict 表示已有进行中的任务（pending 或 running）。
	ErrConflict = errors.New("已有进行中的任务")
)

// Store 内存任务仓库，约束同一时刻仅允许一个活动任务。
type Store struct {
	mu sync.Mutex

	jobs        map[string]*Job
	activeJobID string // 当前 pending 或 running 的任务
}

// NewStore 创建空仓库。
func NewStore() *Store {
	return &Store{
		jobs: make(map[string]*Job),
	}
}

// TryEnqueueBatch 尝试创建任务；若已有活动任务则返回冲突与已有 job。
func (s *Store) TryEnqueueBatch(services []string, action Action) (*Job, *Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activeJobID != "" {
		if j, ok := s.jobs[s.activeJobID]; ok {
			return nil, cloneJob(j), ErrConflict
		}
		s.activeJobID = ""
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	j := &Job{
		ID:        id,
		Services:  append([]string(nil), services...),
		Action:    action,
		Status:    StatusPending,
		CreatedAt: now,
	}
	s.jobs[id] = j
	s.activeJobID = id
	return j, nil, nil
}

// Get 按 ID 查询任务（不存在返回 nil）。
func (s *Store) Get(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return nil
	}
	return cloneJob(j)
}

// MarkRunning 将任务标为运行中。
func (s *Store) MarkRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || j.Status != StatusPending {
		return false
	}
	now := time.Now().UTC()
	j.Status = StatusRunning
	j.StartedAt = &now
	return true
}

// FinishSucceeded 标记成功并释放活动任务占用。
func (s *Store) FinishSucceeded(id, message, logTail string) {
	s.finish(id, StatusSucceeded, message, "", logTail)
}

// FinishSkipped 标记任务已跳过并释放活动任务占用。
func (s *Store) FinishSkipped(id, message, logTail string) {
	s.finish(id, StatusSkipped, message, "", logTail)
}

// FinishFailed 标记失败并释放活动任务占用。
func (s *Store) FinishFailed(id, errMsg, logTail string) {
	s.finish(id, StatusFailed, "", errMsg, logTail)
}

func (s *Store) finish(id string, status Status, message, errMsg, logTail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return
	}
	now := time.Now().UTC()
	j.Status = status
	j.Message = message
	j.Error = errMsg
	j.LogTail = logTail
	j.FinishedAt = &now
	if s.activeJobID == id {
		s.activeJobID = ""
	}
}

func cloneJob(j *Job) *Job {
	cp := *j
	cp.Services = append([]string(nil), j.Services...)
	if j.StartedAt != nil {
		t := *j.StartedAt
		cp.StartedAt = &t
	}
	if j.FinishedAt != nil {
		t := *j.FinishedAt
		cp.FinishedAt = &t
	}
	return &cp
}
