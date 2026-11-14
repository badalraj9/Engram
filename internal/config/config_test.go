package config

// R0 precedence + panel gates (BUILD-PLAN R0 items 3–4): env wins
// over file in both directions, and the panel's fail-closed chain is
// validated before anything can serve.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tomlPath writes a minimal config file and returns its path. Paths
// are forward-slashed (TOML quoted strings are Go-syntax).
func tomlPath(t *testing.T, dataDir, uds string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engram.toml")
	text := "data_dir = \"" + strings.ReplaceAll(dataDir, "\\", "/") + "\"\n" +
		"uds_path = \"" + strings.ReplaceAll(uds, "\\", "/") + "\"\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ENGRAM_DATA_DIR", "ENGRAM_UDS_PATH", "ENGRAM_PANEL_PORT", "ENGRAM_PANEL_TOKEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// TestLoad_EnvWinsOverFile pins R0 gate 3: with a config file present
// AND env pins set, the env values win — the owner's env-pinned
// registrations survive any future config file.
func TestLoad_EnvWinsOverFile(t *testing.T) {
	clearEnv(t)
	path := tomlPath(t, "file-data", "file.sock")
	t.Setenv("ENGRAM_DATA_DIR", "env-data")
	t.Setenv("ENGRAM_UDS_PATH", "env.sock")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "env-data" || cfg.UDSPath != "env.sock" {
		t.Fatalf("env must win: got data=%q uds=%q", cfg.DataDir, cfg.UDSPath)
	}
}

// TestLoad_FileOnly pins the file leg: env unset, the file's values
// load as written.
func TestLoad_FileOnly(t *testing.T) {
	clearEnv(t)
	path := tomlPath(t, "file-data", "file.sock")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "file-data" || cfg.UDSPath != "file.sock" {
		t.Fatalf("file leg: got data=%q uds=%q", cfg.DataDir, cfg.UDSPath)
	}
}

// TestLoad_EnvOnly pins the env leg: no file at all, env alone
// configures (today's registration shape).
func TestLoad_EnvOnly(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENGRAM_DATA_DIR", "env-data")
	t.Setenv("ENGRAM_UDS_PATH", "env.sock")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "env-data" || cfg.UDSPath != "env.sock" {
		t.Fatalf("env leg: got data=%q uds=%q", cfg.DataDir, cfg.UDSPath)
	}
}

// TestValidate_PanelTokenRequired pins the fail-closed chain: a panel
// port without a token is rejected at validation — the panel can
// never come up unauthenticated, even by config mistake.
func TestValidate_PanelTokenRequired(t *testing.T) {
	cfg := Default()
	cfg.PanelPort = 8123
	if err := cfg.Validate(); err == nil {
		t.Fatal("panel port without token must be rejected")
	}
	cfg.PanelToken = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("token set: %v", err)
	}
}

// TestValidate_PanelPortBounds pins the port domain: 0 means off.
func TestValidate_PanelPortBounds(t *testing.T) {
	cfg := Default()
	cfg.PanelPort = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative port must be rejected")
	}
	cfg.PanelPort = 65536
	if err := cfg.Validate(); err == nil {
		t.Fatal("port above 65535 must be rejected")
	}
	cfg.PanelPort = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("port 0 (off) must validate: %v", err)
	}
}
