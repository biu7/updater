package config

import "testing"

func TestLoadFromArgs_UseEnvComposeProjectDirectory(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_DIRECTORY", "/env/host/project")

	cfg, err := LoadFromArgs(nil)
	if err != nil {
		t.Fatalf("LoadFromArgs() error = %v", err)
	}
	if cfg.ComposeProjectDirectory != "/env/host/project" {
		t.Fatalf("ComposeProjectDirectory = %q, want %q", cfg.ComposeProjectDirectory, "/env/host/project")
	}
}

func TestLoadFromArgs_ProjectDirectoryFlagOverridesEnv(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_DIRECTORY", "/env/host/project")

	cfg, err := LoadFromArgs([]string{"--project-directory", "/flag/host/project"})
	if err != nil {
		t.Fatalf("LoadFromArgs() error = %v", err)
	}
	if cfg.ComposeProjectDirectory != "/flag/host/project" {
		t.Fatalf("ComposeProjectDirectory = %q, want %q", cfg.ComposeProjectDirectory, "/flag/host/project")
	}
}
