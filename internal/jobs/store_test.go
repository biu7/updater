package jobs

import "testing"

func TestStore_TryEnqueue_ConflictWhileAnotherComposeTaskRunning(t *testing.T) {
	s := NewStore()
	j1, ex, err := s.TryEnqueueBatch([]string{"web", "api"}, ActionUpdate)
	if err != nil || ex != nil || j1 == nil {
		t.Fatalf("首次入队失败: j=%v ex=%v err=%v", j1, ex, err)
	}
	if j1.Action != ActionUpdate {
		t.Fatalf("action = %q, want %q", j1.Action, ActionUpdate)
	}
	j2, ex2, err2 := s.TryEnqueueBatch([]string{"web", "worker"}, ActionRestart)
	if err2 != ErrConflict || ex2 == nil || j2 != nil {
		t.Fatalf("期望冲突: j2=%v ex2=%v err2=%v", j2, ex2, err2)
	}
	if ex2.ID != j1.ID {
		t.Fatalf("冲突应返回同一任务: %s vs %s", ex2.ID, j1.ID)
	}
	if ex2.Action != ActionUpdate {
		t.Fatalf("existing action = %q, want %q", ex2.Action, ActionUpdate)
	}
}

func TestStore_Finish_ReleasesService(t *testing.T) {
	s := NewStore()
	j, _, _ := s.TryEnqueueBatch([]string{"web", "api"}, ActionUpdate)
	s.MarkRunning(j.ID)
	s.FinishSucceeded(j.ID, "ok", "")

	_, ex, err := s.TryEnqueueBatch([]string{"web", "api"}, ActionRestart)
	if err == ErrConflict {
		t.Fatalf("结束后应可再次入队，但得到冲突 existing=%v", ex)
	}
}

func TestStore_FinishSkipped_ReleasesService(t *testing.T) {
	s := NewStore()
	j, _, _ := s.TryEnqueueBatch([]string{"web", "api"}, ActionUpdate)
	s.MarkRunning(j.ID)
	s.FinishSkipped(j.ID, "skip", "log")

	got := s.Get(j.ID)
	if got == nil {
		t.Fatal("任务不应为空")
	}
	if got.Status != StatusSkipped {
		t.Fatalf("status = %q, want %q", got.Status, StatusSkipped)
	}
	if len(got.Services) != 2 || got.Services[0] != "web" || got.Services[1] != "api" {
		t.Fatalf("services = %#v, want [web api]", got.Services)
	}

	_, ex, err := s.TryEnqueueBatch([]string{"web"}, ActionRestart)
	if err == ErrConflict {
		t.Fatalf("跳过后应可再次入队，但得到冲突 existing=%v", ex)
	}
}
