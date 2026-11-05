package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/biu7/updater/internal/config"
)

const (
	remoteIndex = "sha256:aaaa000000000000000000000000000000000000000000000000000000000001"
	oldIndex    = "sha256:aaaa000000000000000000000000000000000000000000000000000000000000"
)

type fakeCheck struct {
	running   map[string][]string       // service -> container image IDs
	images    map[string]map[string]any // image ID -> inspect JSON object
	remote    map[string]string         // ref -> imagetools JSON
	remoteErr map[string]error          // ref -> error
	refs      map[string]string         // service -> ref
}

func (f *fakeCheck) runner() *Runner {
	return &Runner{
		cfg: config.Config{},
		composeConfigRootFn: func(ctx context.Context) (map[string]any, error) {
			services := map[string]any{}
			for svc, ref := range f.refs {
				services[svc] = map[string]any{"image": ref}
			}
			return map[string]any{"services": services}, nil
		},
		runningServiceImageIDsFn: func(ctx context.Context, service string) ([]string, error) {
			return f.running[service], nil
		},
		outputFn: func(ctx context.Context, args []string) ([]byte, error) {
			switch {
			case len(args) >= 2 && args[0] == "image" && args[1] == "inspect" && args[2] == "--":
				var out []map[string]any
				for _, id := range args[3:] { // image inspect -- <ids…>
					img, ok := f.images[id]
					if !ok {
						return nil, fmt.Errorf("no such image %s", id)
					}
					out = append(out, img)
				}
				return json.Marshal(out)
			case len(args) >= 3 && args[0] == "buildx" && args[1] == "imagetools":
				ref := args[len(args)-1]
				if err := f.remoteErr[ref]; err != nil {
					return nil, err
				}
				return []byte(f.remote[ref]), nil
			}
			return nil, fmt.Errorf("unexpected command %v", args)
		},
	}
}

func multiPlatformRemote(digest, version, ibVersion string) string {
	env := []string{"PATH=/bin", "SECRET_TOKEN=should-not-leak"}
	if ibVersion != "" {
		env = append(env, "IB_GATEWAY_VERSION="+ibVersion)
	}
	cfg := map[string]any{"config": map[string]any{
		"Env":    env,
		"Labels": map[string]string{"org.opencontainers.image.version": version},
	}}
	b, _ := json.Marshal(map[string]any{
		"name":     "x",
		"manifest": map[string]any{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": digest},
		"image":    map[string]any{runtime.GOOS + "/" + runtime.GOARCH: cfg, "linux/zzz": cfg},
	})
	return string(b)
}

func singlePlatformRemote(digest, version string) string {
	b, _ := json.Marshal(map[string]any{
		"manifest": map[string]any{"mediaType": "application/vnd.docker.distribution.manifest.v2+json", "digest": digest},
		"image": map[string]any{"architecture": "amd64", "config": map[string]any{
			"Labels": map[string]string{"org.opencontainers.image.version": version},
		}},
	})
	return string(b)
}

func inspectedImage(id string, repoDigests []string, version string, env ...string) map[string]any {
	return map[string]any{
		"Id":          id,
		"RepoDigests": repoDigests,
		"Config": map[string]any{
			"Env":    append([]string{"SECRET_TOKEN=should-not-leak"}, env...),
			"Labels": map[string]string{"org.opencontainers.image.version": version},
		},
	}
}

func checkOne(t *testing.T, f *fakeCheck, service string) CheckResult {
	t.Helper()
	res := f.runner().CheckServices(context.Background(), []string{service})
	if len(res) != 1 {
		t.Fatalf("results = %#v", res)
	}
	b, _ := json.Marshal(res[0])
	if strings.Contains(string(b), "SECRET_TOKEN") || strings.Contains(string(b), "should-not-leak") {
		t.Fatalf("env leaked: %s", b)
	}
	return res[0]
}

func wantAvailable(t *testing.T, r CheckResult, want *bool, code string) {
	t.Helper()
	if (r.UpdateAvailable == nil) != (want == nil) || (want != nil && *r.UpdateAvailable != *want) || r.ErrorCode != code {
		t.Fatalf("result = %+v, want available=%v code=%q", r, want, code)
	}
}

func ptr(b bool) *bool { return &b }

func TestCheck_ClassicStoreRepoDigests(t *testing.T) {
	ref := "ghcr.io/x/trader:latest"
	f := &fakeCheck{
		refs:    map[string]string{"trader": ref},
		running: map[string][]string{"trader": {"sha256:cfg1"}},
		images:  map[string]map[string]any{"sha256:cfg1": inspectedImage("sha256:cfg1", []string{"ghcr.io/x/trader@" + remoteIndex}, "v1.2.0")},
		remote:  map[string]string{ref: singlePlatformRemote(remoteIndex, "v1.2.0")},
	}
	r := checkOne(t, f, "trader")
	wantAvailable(t, r, ptr(false), "")
	if r.Image != ref || r.CurrentVersion != "v1.2.0" || r.RemoteVersion != "v1.2.0" {
		t.Fatalf("result = %+v", r)
	}

	f.images["sha256:cfg1"] = inspectedImage("sha256:cfg1", []string{"ghcr.io/x/trader@" + oldIndex}, "v1.1.0")
	f.remote[ref] = singlePlatformRemote(remoteIndex, "v1.2.0")
	r = checkOne(t, f, "trader")
	wantAvailable(t, r, ptr(true), "")
	if r.CurrentVersion != "v1.1.0" || r.RemoteVersion != "v1.2.0" {
		t.Fatalf("result = %+v", r)
	}
}

func TestCheck_ContainerdStoreImageIDIsIndexDigest(t *testing.T) {
	ref := "ghcr.io/gnzsnz/ib-gateway:stable"
	f := &fakeCheck{
		refs:    map[string]string{"ib-gateway": ref},
		running: map[string][]string{"ib-gateway": {remoteIndex}},
		images:  map[string]map[string]any{remoteIndex: inspectedImage(remoteIndex, nil, "label-ver", "IB_GATEWAY_VERSION=10.50.1f")},
		remote:  map[string]string{ref: multiPlatformRemote(remoteIndex, "label-ver", "10.50.1f")},
	}
	r := checkOne(t, f, "ib-gateway")
	wantAvailable(t, r, ptr(false), "")
	if r.CurrentVersion != "10.50.1f" || r.RemoteVersion != "10.50.1f" {
		t.Fatalf("IB_GATEWAY_VERSION should take precedence: %+v", r)
	}

	f.running["ib-gateway"] = []string{oldIndex}
	f.images[oldIndex] = inspectedImage(oldIndex, nil, "", "IB_GATEWAY_VERSION=10.49.1a")
	f.remote[ref] = multiPlatformRemote(remoteIndex, "", "10.50.1f")
	r = checkOne(t, f, "ib-gateway")
	wantAvailable(t, r, ptr(true), "")
	if r.CurrentVersion != "10.49.1a" {
		t.Fatalf("result = %+v", r)
	}
}

func TestCheck_AnyContainerOnOtherImageIsUpdateAvailable(t *testing.T) {
	ref := "ghcr.io/x/trader:latest"
	f := &fakeCheck{
		refs:    map[string]string{"trader": ref},
		running: map[string][]string{"trader": {remoteIndex, oldIndex}},
		images: map[string]map[string]any{
			remoteIndex: inspectedImage(remoteIndex, nil, "v2"),
			oldIndex:    inspectedImage(oldIndex, nil, "v1"),
		},
		remote: map[string]string{ref: singlePlatformRemote(remoteIndex, "v2")},
	}
	wantAvailable(t, checkOne(t, f, "trader"), ptr(true), "")
}

func TestCheck_RemoteFailuresAreNullWithCode(t *testing.T) {
	ref := "ghcr.io/x/trader:latest"
	cases := []struct {
		name string
		err  error
		out  string
		code string
	}{
		{"auth", errors.New("exit status 1: ERROR: failed to authorize: 403 Forbidden"), "", CheckErrRemoteAuth},
		{"notfound", errors.New("exit status 1: ERROR: ghcr.io/x/trader:latest: not found"), "", CheckErrRemoteNotFound},
		{"timeout", context.DeadlineExceeded, "", CheckErrRemoteTimeout},
		{"network", errors.New("exit status 1: dial tcp: lookup ghcr.io: no such host"), "", CheckErrRemoteUnavailable},
		{"garbage", nil, "not json", CheckErrRemoteInvalidOutput},
		{"unauthorized", errors.New("exit status 1: ERROR: unexpected status: 401 Unauthorized"), "", CheckErrRemoteAuth},
		{"authRequired", errors.New("exit status 1: authentication required"), "", CheckErrRemoteAuth},
		{"scope", errors.New("exit status 1: error: insufficient_scope: authorization failed"), "", CheckErrRemoteAuth},
		{"digitsInDigest", errors.New("exit status 1: dial tcp 10.0.4013.1:443: connection refused sha256:a401b"), "", CheckErrRemoteUnavailable},
		{"404", errors.New("exit status 1: unexpected status code 404"), "", CheckErrRemoteNotFound},
		{"manifestUnknown", errors.New("exit status 1: MANIFEST_UNKNOWN: manifest unknown"), "", CheckErrRemoteNotFound},
		{"localPermission", errors.New("exit status 1: permission denied while trying to connect to the docker API at unix:///var/run/docker.sock"), "", CheckErrRemoteUnavailable},
		{"noDocker", fmt.Errorf("wrap: %w", &exec.Error{Name: "docker", Err: exec.ErrNotFound}), "", CheckErrRemoteUnavailable},
		{"noDockerText", errors.New(`exec: "docker": executable file not found in $PATH`), "", CheckErrRemoteUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCheck{
				refs:      map[string]string{"trader": ref},
				running:   map[string][]string{"trader": {"sha256:cfg1"}},
				images:    map[string]map[string]any{"sha256:cfg1": inspectedImage("sha256:cfg1", []string{"ghcr.io/x/trader@" + oldIndex}, "v1")},
				remote:    map[string]string{ref: tc.out},
				remoteErr: map[string]error{ref: tc.err},
			}
			r := checkOne(t, f, "trader")
			wantAvailable(t, r, nil, tc.code)
			if r.CurrentVersion != "v1" || r.RemoteVersion != "" {
				t.Fatalf("result = %+v", r)
			}
			b, _ := json.Marshal(r)
			if !strings.Contains(string(b), `"update_available":null`) {
				t.Fatalf("update_available must serialize as null: %s", b)
			}
		})
	}
}

func TestCheck_NotRunningAndMissingImage(t *testing.T) {
	ref := "ghcr.io/x/trader:latest"
	f := &fakeCheck{
		refs:   map[string]string{"trader": ref},
		images: map[string]map[string]any{},
		remote: map[string]string{ref: singlePlatformRemote(remoteIndex, "v2")},
	}
	r := checkOne(t, f, "trader")
	wantAvailable(t, r, nil, CheckErrNotRunning)
	if r.RemoteVersion != "v2" {
		t.Fatalf("remote version should still be reported: %+v", r)
	}

	f.running = map[string][]string{"trader": {"sha256:gone"}}
	wantAvailable(t, checkOne(t, f, "trader"), nil, CheckErrLocalInspect)

	res := f.runner().CheckServices(context.Background(), []string{"missing"})
	if res[0].ErrorCode != CheckErrImageRefMissing || res[0].UpdateAvailable != nil {
		t.Fatalf("result = %+v", res[0])
	}
}

func TestCheck_DeadlineDuringLocalInspectIsTimeout(t *testing.T) {
	ref := "ghcr.io/x/trader:latest"
	f := &fakeCheck{
		refs:      map[string]string{"trader": ref},
		running:   map[string][]string{"trader": {"sha256:cfg1"}},
		images:    map[string]map[string]any{},
		remoteErr: map[string]error{ref: context.DeadlineExceeded},
	}
	ctx, cancel := context.WithTimeout(context.Background(), -1)
	defer cancel()
	res := f.runner().CheckServices(ctx, []string{"trader"})
	if res[0].ErrorCode != CheckErrTimeout || res[0].UpdateAvailable != nil {
		t.Fatalf("result = %+v", res[0])
	}
}

func TestCheck_ImageRefsPassedAfterDoubleDash(t *testing.T) {
	var got [][]string
	r := &Runner{outputFn: func(ctx context.Context, args []string) ([]byte, error) {
		got = append(got, args)
		return nil, errors.New("x")
	}}
	_, _, _ = r.remoteImage(context.Background(), "-evil")
	_, _ = r.localImageIDExec(context.Background(), "-evil")
	for _, args := range got {
		if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != "-evil" {
			t.Fatalf("ref must follow --: %v", args)
		}
	}
	if len(got) != 2 {
		t.Fatalf("calls = %v", got)
	}
}
