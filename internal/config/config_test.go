package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("expected :8080, got %s", cfg.Server.ListenAddr)
	}
	if cfg.Auth.CookieName != "savras_session" {
		t.Errorf("expected savras_session, got %s", cfg.Auth.CookieName)
	}
	if cfg.Auth.JwtExpiryDuration != 24*time.Hour {
		t.Errorf("expected 24h, got %v", cfg.Auth.JwtExpiryDuration)
	}
}

func TestLoadConfig_FromYAML(t *testing.T) {
	dir := t.TempDir()
	yaml := `
server:
  listen_addr: ":9090"
auth:
  jwt_expiry: "1h"
  cookie_name: "test_cookie"
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644)
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.Server.ListenAddr != ":9090" {
		t.Errorf("expected :9090, got %s", cfg.Server.ListenAddr)
	}
}

func TestLoadConfig_FromEnv(t *testing.T) {
	os.Setenv("SAVRAS_SERVER_LISTEN_ADDR", ":7070")
	defer os.Unsetenv("SAVRAS_SERVER_LISTEN_ADDR")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.Server.ListenAddr != ":7070" {
		t.Errorf("expected :7070, got %s", cfg.Server.ListenAddr)
	}
}
