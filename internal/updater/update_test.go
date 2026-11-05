package updater

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/biu7/updater/internal/config"
)

// fakeDocker 模拟 compose 项目状态：本地 tag 指向的镜像 ID、远端最新镜像 ID、各服务运行容器的镜像。
type fakeDocker struct {
	refs      map[string]string   // service -> image ref
	local     map[string]string   // ref -> 本地 tag 镜像 ID（缺失表示本地无此镜像）
	remote    map[string]string   // ref -> pull 后得到的镜像 ID
	running   map[string][]string // service -> 运行容器镜像 ID
	localErr  map[string]bool     // ref -> image inspect 失败
	psErr     map[string]bool     // service -> ps/inspect 失败
	failUp    bool
	failPull  bool
	cmds      [][]string
	cfgFailed bool
}

func (f *fakeDocker) runner() *Runner {
	return &Runner{
		cfg: config.Config{ComposeFiles: []string{"docker-compose.yml"}},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			if f.cfgFailed {
				return nil, errors.New("config unavailable")
			}
			services := map[string]any{}
			for svc, ref := range f.refs {
				if ref == "" {
					services[svc] = map[string]any{"build": map[string]any{"context": "."}}
				} else {
					services[svc] = map[string]any{"image": ref}
				}
			}
			return map[string]any{"services": services}, nil
		},
		localImageIDFn: func(ctx context.Context, ref string) (string, error) {
			if f.localErr[ref] {
				return "", errors.New("inspect failed")
			}
			id, ok := f.local[ref]
			if !ok {
				return "", errors.New("No such image")
			}
			return id, nil
		},
		runningServiceImageIDsFn: func(ctx context.Context, service string) ([]string, error) {
			if f.psErr[service] {
				return nil, errors.New("ps failed")
			}
			return normalizeImageIDs(f.running[service]), nil
		},
		runFn: func(ctx context.Context, args []string, w io.Writer) error {
			f.cmds = append(f.cmds, slices.Clone(args))
			// args: compose -f docker-compose.yml <sub> ...
			sub := args[3]
			switch sub {
			case "pull":
				if f.failPull {
					return errors.New("pull failed")
				}
				for _, svc := range args[4:] {
					if ref := f.refs[svc]; ref != "" {
						if id, ok := f.remote[ref]; ok {
							f.local[ref] = id
						}
					}
				}
			case "up":
				if f.failUp {
					return errors.New("up failed")
				}
				for _, svc := range args[6:] {
					f.running[svc] = []string{f.local[f.refs[svc]]}
				}
			}
			return nil
		},
	}
}

func newFake() *fakeDocker {
	return &fakeDocker{
		refs:    map[string]string{"trader": "ghcr.io/x/trader:latest", "ib-gateway": "ghcr.io/x/ibg:stable", "db": "postgres:17"},
		local:   map[string]string{"ghcr.io/x/trader:latest": "sha256:t1", "ghcr.io/x/ibg:stable": "sha256:g1", "postgres:17": "sha256:p1"},
		remote:  map[string]string{"ghcr.io/x/trader:latest": "sha256:t1", "ghcr.io/x/ibg:stable": "sha256:g1", "postgres:17": "sha256:p2"},
		running: map[string][]string{"trader": {"sha256:t1"}, "ib-gateway": {"sha256:g1"}, "db": {"sha256:p1"}},
	}
}

var composePrefix = []string{"compose", "-f", "docker-compose.yml"}

func cmd(parts ...string) []string { return append(slices.Clone(composePrefix), parts...) }

func TestUpdateServices_PullsAndUpsOnlyRequestedChangedServicesWithNoDeps(t *testing.T) {
	f := newFake()
	f.remote["ghcr.io/x/trader:latest"] = "sha256:t2"
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader", "ib-gateway"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{cmd("pull", "trader", "ib-gateway"), cmd("up", "-d", "--no-deps", "trader")}
	if !reflect.DeepEqual(f.cmds, want) {
		t.Fatalf("cmds = %#v, want %#v", f.cmds, want)
	}
	if !reflect.DeepEqual(res.NeedsUp, []string{"trader"}) || !reflect.DeepEqual(res.Upped, []string{"trader"}) || len(res.Uncertain) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Message, "更新已完成") || !strings.Contains(res.Log, "sha256:t1 -> sha256:t2") {
		t.Fatalf("message/log = %q / %q", res.Message, res.Log)
	}
}

func TestUpdateServices_NoChangeSkipsUp(t *testing.T) {
	f := newFake()
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.cmds, [][]string{cmd("pull", "trader")}) {
		t.Fatalf("cmds = %#v", f.cmds)
	}
	if len(res.NeedsUp)+len(res.Upped)+len(res.Uncertain) != 0 || res.NeedsUp == nil || res.Upped == nil || res.Uncertain == nil {
		t.Fatalf("result = %#v", res)
	}
	if !strings.Contains(res.Message, "未变化") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestUpdateServices_PullFalseDoesNotPull(t *testing.T) {
	f := newFake()
	f.local["ghcr.io/x/trader:latest"] = "sha256:t2" // 之前已 pull 但未 up
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.cmds, [][]string{cmd("up", "-d", "--no-deps", "trader")}) {
		t.Fatalf("cmds = %#v", f.cmds)
	}
	if !reflect.DeepEqual(res.Upped, []string{"trader"}) {
		t.Fatalf("result = %+v", res)
	}
}

// 已 pull 未 up（例如 /pull 后放弃、或上次 up 失败）后再次 update（pull=true）仍须完成 up。
func TestUpdateServices_PulledButNotUppedCompletesOnRetry(t *testing.T) {
	f := newFake()
	f.remote["ghcr.io/x/trader:latest"] = "sha256:t2"
	r := f.runner()
	pulled, err := r.PullServices(context.Background(), []string{"trader"})
	if err != nil || !reflect.DeepEqual(pulled.NeedsUp, []string{"trader"}) {
		t.Fatalf("pull = %+v err=%v", pulled, err)
	}
	// 第一次 update 的 up 失败
	f.failUp = true
	if _, err := r.UpdateServices(context.Background(), []string{"trader"}, true); err == nil {
		t.Fatal("expected up failure")
	}
	f.failUp = false
	f.cmds = nil
	// 第二次 pull 前后 ID 相同（都是 t2），但运行容器仍为 t1，必须 up
	res, err := r.UpdateServices(context.Background(), []string{"trader"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{cmd("pull", "trader"), cmd("up", "-d", "--no-deps", "trader")}
	if !reflect.DeepEqual(f.cmds, want) || !reflect.DeepEqual(res.Upped, []string{"trader"}) {
		t.Fatalf("cmds = %#v result = %+v", f.cmds, res)
	}
	if !reflect.DeepEqual(f.running["trader"], []string{"sha256:t2"}) {
		t.Fatalf("running = %v", f.running["trader"])
	}
}

func TestUpdateServices_NoRunningContainerNeedsUp(t *testing.T) {
	f := newFake()
	delete(f.running, "ib-gateway")
	res, err := f.runner().UpdateServices(context.Background(), []string{"ib-gateway"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Upped, []string{"ib-gateway"}) {
		t.Fatalf("result = %+v", res)
	}
}

func TestUpdateServices_AnyContainerOnOldImageNeedsUp(t *testing.T) {
	f := newFake()
	f.running["trader"] = []string{"sha256:t1", "sha256:t0"}
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader"}, false)
	if err != nil || !reflect.DeepEqual(res.Upped, []string{"trader"}) {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestUpdateServices_UncertainServicesAreNotUpped(t *testing.T) {
	f := newFake()
	f.refs["builder"] = "" // 仅 build，无 image
	f.remote["ghcr.io/x/trader:latest"] = "sha256:t2"
	f.psErr = map[string]bool{"ib-gateway": true}
	f.localErr = map[string]bool{"postgres:17": true}
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader", "ib-gateway", "db", "builder"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{cmd("pull", "trader", "ib-gateway", "db", "builder"), cmd("up", "-d", "--no-deps", "trader")}
	if !reflect.DeepEqual(f.cmds, want) {
		t.Fatalf("cmds = %#v", f.cmds)
	}
	if !reflect.DeepEqual(res.Upped, []string{"trader"}) || !reflect.DeepEqual(res.Uncertain, []string{"ib-gateway", "db", "builder"}) {
		t.Fatalf("result = %+v", res)
	}
}

func TestUpdateServices_OnlyUncertainSkips(t *testing.T) {
	f := newFake()
	f.cfgFailed = true
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Upped) != 0 || !reflect.DeepEqual(res.Uncertain, []string{"trader"}) || !strings.Contains(res.Message, "无法确认") {
		t.Fatalf("result = %+v", res)
	}
	if !reflect.DeepEqual(f.cmds, [][]string{cmd("pull", "trader")}) {
		t.Fatalf("cmds = %#v", f.cmds)
	}
}

func TestUpdateServices_UpFailureReturnsErrorWithStructuredResult(t *testing.T) {
	f := newFake()
	f.remote["ghcr.io/x/trader:latest"] = "sha256:t2"
	f.failUp = true
	res, err := f.runner().UpdateServices(context.Background(), []string{"trader"}, true)
	if err == nil || !strings.Contains(err.Error(), "docker compose up -d") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(res.NeedsUp, []string{"trader"}) || len(res.Upped) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestPullServices_OnlyPullsAndReportsNeedsUp(t *testing.T) {
	f := newFake()
	f.psErr = map[string]bool{"ib-gateway": true}
	res, err := f.runner().PullServices(context.Background(), []string{"db", "ib-gateway"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.cmds, [][]string{cmd("pull", "db", "ib-gateway")}) {
		t.Fatalf("cmds = %#v", f.cmds)
	}
	if !reflect.DeepEqual(res.NeedsUp, []string{"db"}) || !reflect.DeepEqual(res.Uncertain, []string{"ib-gateway"}) || len(res.Upped) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestPullServices_PullFailure(t *testing.T) {
	f := newFake()
	f.failPull = true
	if _, err := f.runner().PullServices(context.Background(), []string{"db"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateServices(t *testing.T) {
	f := newFake()
	r := f.runner()
	r.cfg.AllowedServices = map[string]struct{}{"trader": {}, "ib-gateway": {}, "ghost": {}}
	if err := r.ValidateServices(context.Background(), []string{"trader", "ib-gateway"}); err != nil {
		t.Fatalf("valid subset rejected: %v", err)
	}
	for _, bad := range [][]string{{"db"}, {"ghost"}, {"trader", "nope"}} {
		if err := r.ValidateServices(context.Background(), bad); !errors.Is(err, ErrInvalidServices) {
			t.Fatalf("%v: err = %v, want ErrInvalidServices", bad, err)
		}
	}
	f.cfgFailed = true
	if err := r.ValidateServices(context.Background(), []string{"trader"}); err == nil || errors.Is(err, ErrInvalidServices) {
		t.Fatalf("config failure should be a non-validation error: %v", err)
	}
}
