package main

// STEP 0 gates (BUILD-PLAN STEP 0 ruling, 2026-11-14): the data dir
// derives the socket. A holder started with ONLY ENGRAM_DATA_DIR
// pinned binds <data>/engram.sock, and `engramd -tick N` with the
// same single pin must find it — the CWD-relative ./engram.sock hunt
// is gone. When no holder exists, the failure names the derived path
// it actually tried.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTick_FindsHolderByDataDir: bare serve holder + -tick, both with
// only ENGRAM_DATA_DIR — the tick must reach the holder through the
// derived socket.
func TestTick_FindsHolderByDataDir(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	sock := filepath.Join(data, "engram.sock") // the derived path

	holder := startChildData(t, "", data) // bare serve, NO UDS pin
	awaitHolder(t, sock, holder)

	tick := startChildData(t, "-tick=1", data)
	select {
	case err := <-tick.done:
		if err != nil {
			t.Fatalf("tick over derived socket must succeed: %v (stderr %q)",
				err, tick.stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("tick never finished (stderr %q)", tick.stderr.String())
	}
}

// TestTick_MissingHolderNamesDerivedPath: no holder anywhere — the
// tick exits 1 and the error names the derived path it tried (never
// a bare ./engram.sock).
func TestTick_MissingHolderNamesDerivedPath(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	sock := filepath.Join(data, "engram.sock")

	tick := startChildData(t, "-tick=1", data)
	var waitErr error
	select {
	case waitErr = <-tick.done:
	case <-time.After(20 * time.Second):
		t.Fatal("tick never exited")
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("tick with no holder must exit 1, got %v (stderr %q)", waitErr, tick.stderr.String())
	}
	if !strings.Contains(tick.stderr.String(), sock) {
		t.Fatalf("error must name the derived socket path %s, stderr: %q", sock, tick.stderr.String())
	}
	if strings.Contains(tick.stderr.String(), "./engram.sock") {
		t.Fatalf("error must not hunt a CWD-relative socket: %q", tick.stderr.String())
	}
}
