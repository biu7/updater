// Package jobs 维护内存中的异步更新任务及按服务串行的占用关系。
package jobs

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrConflict 表示该服务已有进行中的任务（pending 或 running）。
	ErrConflict = errors.New("服务已有进行中的更新任务")
)

// Store 内存任务仓库，按服务串行约束同一时刻仅一个活动任务。
type Store struct {
	mu sync.Mutex

	jobs            map[string]*Job
	activeByService map[string]string // service -> jobID（pending 或 running）
}

// NewStore 创建空仓库。
func NewStore() *Store {
	return &Store{
		jobs:            make(map[string]*Job),
		activeByService: make(map[string]string),
	}
}

// TryEnqueue 尝试为指定服务创建任务；若已有活动任务则返回冲突与已有 job。
func (s *Store) TryEnqueue(service string) (*Job, *Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existingID, ok := s.activeByService[service]; ok {
		if j, ok2 := s.jobs[existingID]; ok2 {
			return nil, j, ErrConflict
		}
		delete(s.activeByService, service)
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	j := &Job{
		ID:        id,
		Service:   service,
		Status:    StatusPending,
		CreatedAt: now,
	}
	s.jobs[id] = j
	s.activeByService[service] = id
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

// FinishSucceeded 标记成功并释放服务占用。
func (s *Store) FinishSucceeded(id, message, logTail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return
	}
	now := time.Now().UTC()
	j.Status = StatusSucceeded
	j.Message = message
	j.LogTail = logTail
	j.FinishedAt = &now
	delete(s.activeByService, j.Service)
}

// FinishFailed 标记失败并释放服务占用。
func (s *Store) FinishFailed(id, errMsg, logTail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return
	}
	now := time.Now().UTC()
	j.Status = StatusFailed
	j.Error = errMsg
	j.LogTail = logTail
	j.FinishedAt = &now
	delete(s.activeByService, j.Service)
}

func cloneJob(j *Job) *Job {
	cp := *j
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
