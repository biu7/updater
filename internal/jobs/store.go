// Package jobs 维护内存中的异步更新任务及串行执行关系。
package jobs

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrConflict 表示已有进行中的任务（pending 或 running）。
	ErrConflict = errors.New("已有进行中的任务")
)

// MaxHistory 为内存中保留的任务条数上限（含进行中任务）。
const MaxHistory = 20

// Store 内存任务仓库，约束同一时刻仅允许一个活动任务，只保留最近 MaxHistory 条。
type Store struct {
	mu sync.Mutex

	jobs        map[string]*Job
	order       []string // 按创建先后排列的任务 ID
	activeJobID string   // 当前 pending 或 running 的任务
}

// Outcome 是任务结束时写入的结果。
type Outcome struct {
	Message   string
	Error     string
	LogTail   string
	NeedsUp   []string
	Upped     []string
	Uncertain []string
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
	s.order = append(s.order, id)
	s.activeJobID = id
	for len(s.order) > MaxHistory {
		delete(s.jobs, s.order[0])
		s.order = s.order[1:]
	}
	return cloneJob(j), nil, nil
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

// Latest 返回包含指定服务的最近一个任务（不存在返回 nil）。
func (s *Store) Latest(service string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		j := s.jobs[s.order[i]]
		if j != nil && slices.Contains(j.Services, service) {
			return cloneJob(j)
		}
	}
	return nil
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

// Finish 以给定状态结束任务并释放活动任务占用。
func (s *Store) Finish(id string, status Status, o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return
	}
	now := time.Now().UTC()
	j.Status = status
	j.Message = o.Message
	j.Error = o.Error
	j.LogTail = o.LogTail
	j.NeedsUp = slices.Clone(o.NeedsUp)
	j.Upped = slices.Clone(o.Upped)
	j.Uncertain = slices.Clone(o.Uncertain)
	j.FinishedAt = &now
	if s.activeJobID == id {
		s.activeJobID = ""
	}
}

// FinishSucceeded 标记成功并释放活动任务占用。
func (s *Store) FinishSucceeded(id, message, logTail string) {
	s.Finish(id, StatusSucceeded, Outcome{Message: message, LogTail: logTail})
}

// FinishSkipped 标记任务已跳过并释放活动任务占用。
func (s *Store) FinishSkipped(id, message, logTail string) {
	s.Finish(id, StatusSkipped, Outcome{Message: message, LogTail: logTail})
}

// FinishFailed 标记失败并释放活动任务占用。
func (s *Store) FinishFailed(id, errMsg, logTail string) {
	s.Finish(id, StatusFailed, Outcome{Error: errMsg, LogTail: logTail})
}

func cloneJob(j *Job) *Job {
	cp := *j
	cp.Services = append([]string(nil), j.Services...)
	cp.NeedsUp = append([]string{}, j.NeedsUp...)
	cp.Upped = append([]string{}, j.Upped...)
	cp.Uncertain = append([]string{}, j.Uncertain...)
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
