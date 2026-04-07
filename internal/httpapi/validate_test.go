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
