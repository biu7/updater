package updater

import "testing"

func TestImageRefFromComposeConfig(t *testing.T) {
	root := map[string]any{
		"services": map[string]any{
			"spread": map[string]any{
				"image": "registry.example/spread:v0.0.65",
			},
			"web": map[string]any{
				"build": map[string]any{"context": "."},
			},
		},
	}
	ref, ok := imageRefFromComposeConfig(root, "spread")
	if !ok || ref != "registry.example/spread:v0.0.65" {
		t.Fatalf("spread: got %q %v", ref, ok)
	}
	_, ok = imageRefFromComposeConfig(root, "web")
	if ok {
		t.Fatal("web 仅 build 时不应有 image")
	}
	_, ok = imageRefFromComposeConfig(root, "missing")
	if ok {
		t.Fatal("不存在的服务应失败")
	}
}

func TestServiceNamesFromComposeConfig(t *testing.T) {
	root := map[string]any{
		"services": map[string]any{
			"worker": map[string]any{},
			" api ":  map[string]any{},
			"":       map[string]any{},
		},
	}
	got := serviceNamesFromComposeConfig(root)
	if len(got) != 2 || got[0] != "api" || got[1] != "worker" {
		t.Fatalf("serviceNamesFromComposeConfig() = %#v, want [api worker]", got)
	}
}
