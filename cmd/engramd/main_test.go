// Tick-runner proof (Q2 setup: the dogfood tick path must work through
// the live socket — including the durable-tick reject on re-runs).
package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/daemon"
)

// openTickDaemon boots a daemon like the other harnesses and returns a
// config file pointing at its socket.
func openTickDaemon(t *testing.T) string {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir() + "/data"
	cfg.RateLimitPerSec = 100000
	d, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock := t.TempDir() + "/engram.sock"
	go func() { _ = d.Serve(sock) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { d.Stop(); d.Close() })
	// TOML quoted strings are Go-syntax: forward slashes survive
	// Windows temp dirs without escaping.
	fix := func(p string) string { return strings.ReplaceAll(p, "\\", "/") }
	cfgText := "data_dir = \"" + fix(cfg.DataDir) + "\"\nuds_path = \"" + fix(sock) + "\"\n"
	path := t.TempDir() + "/engram.toml"
	if err := os.WriteFile(path, []byte(cfgText), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTick_RunsThroughSocket ticks the live daemon twice: first
// accepted, second rejected (durable tick — same number never runs
// twice).
func TestTick_RunsThroughSocket(t *testing.T) {
	cfgPath := openTickDaemon(t)
	if err := runTick(cfgPath, 0); err != nil {
		t.Fatalf("tick 0: %v", err)
	}
	if err := runTick(cfgPath, 0); err == nil {
		t.Fatal("re-tick 0 must be rejected (durable tick)")
	}
	if err := runTick(cfgPath, 1); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
}

// TestTick_NoSocketFailsClosed: no daemon means an error, never a
// phantom tick.
func TestTick_NoSocketFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/engram.toml"
	text := "data_dir = \"" + strings.ReplaceAll(dir, "\\", "/") + "/data\"\nuds_path = \"" +
		strings.ReplaceAll(dir, "\\", "/") + "/missing.sock\"\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runTick(path, 0); err == nil {
		t.Fatal("tick with no daemon must error")
	}
}
