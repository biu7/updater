package httpapi

import "testing"

func TestValidateServiceName(t *testing.T) {
	if err := ValidateServiceName(""); err == nil {
		t.Fatal("空名应失败")
	}
	if err := ValidateServiceName("web"); err != nil {
		t.Fatalf("web 应合法: %v", err)
	}
	if err := ValidateServiceName("app_v2"); err != nil {
		t.Fatalf("app_v2 应合法: %v", err)
	}
	if err := ValidateServiceName("-bad"); err == nil {
		t.Fatal("-bad 应失败")
	}
}

func TestNormalizeServices(t *testing.T) {
	got, err := NormalizeServices([]string{"web", " api ", "web"})
	if err != nil {
		t.Fatalf("NormalizeServices() error = %v", err)
	}
	if len(got) != 2 || got[0] != "web" || got[1] != "api" {
		t.Fatalf("NormalizeServices() = %#v, want [web api]", got)
	}
}

func TestNormalizeServices_Empty(t *testing.T) {
	if _, err := NormalizeServices(nil); err == nil {
		t.Fatal("空列表应失败")
	}
}
