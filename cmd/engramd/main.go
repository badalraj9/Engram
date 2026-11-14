// Command engramd runs the Engram memory daemon (P6 — BUILD-RANKS E;
// Q1 adds the MCP modes; R0 adds holder takeover to --mcp).
//
// Three modes, one binary:
//
//	engramd               serve the UDS socket only (no-MCP users; stdout clean)
//	engramd --mcp         stdio MCP translator: connects to a live holder,
//	                      or — when none answers — becomes the holder
//	                      (same Open→Serve path as --both; lock-gated;
//	                      losing the store race falls back to dialing)
//	engramd --both        co-start: one process opens the store once and
//	                      serves socket + stdio together (one command start)
//
// Thin shell by design: flags, config, open/dial, serve to signal or
// stdio EOF, drain, close. Zero domain logic. The host owns the clock
// and the lifecycle; this binary never schedules itself (FR10).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	goClient "engram/client/go"
	"engram/internal/config"
	"engram/internal/daemon"
	"engram/internal/mcpshim"
)

// dialRetryWindow bounds how long a process that LOST the store race
// waits for the winner's socket (bug report 2026-11-14: two --mcp
// starters racing on a fresh dir must both survive — the loser dials
// the winner; if no holder ever serves, the loser exits non-zero
// within ~5s instead of hanging).
const (
	dialRetryWindow   = 5 * time.Second
	dialRetryInterval = 50 * time.Millisecond
)

func main() {
	if err := run(); err != nil {
		log.Printf("engramd: %v", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "TOML config path (empty = defaults + ENGRAM_* env)")
	mcp := flag.Bool("mcp", false, "stdio MCP shim only (dials a live socket, opens no store)")
	both := flag.Bool("both", false, "co-start: serve socket + stdio MCP in one process")
	tick := flag.Int64("tick", -1, "one-shot maintenance tick N via the live socket (daemon must run; prints counts)")
	flag.Parse()
	modes := 0
	for _, on := range []bool{*mcp, *both, *tick >= 0} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return fmt.Errorf("--mcp, --both, and --tick are exclusive")
	}
	switch {
	case *mcp:
		return runShim(*configPath)
	case *both:
		return runBoth(*configPath)
	case *tick >= 0:
		return runTick(*configPath, *tick)
	default:
		return runServe(*configPath)
	}
}

// runServe is the P6 serve-only path, unchanged.
func runServe(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	d, err := daemon.Open(cfg)
	if err != nil {
		return err
	}
	defer func() {
		_ = d.Close()
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Serve(cfg.UDSPath)
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	d.Stop()
	if err := <-errCh; err != nil {
		return err
	}
	return nil
}

// runShim — Q1 --mcp mode, R0 takeover (BUILD-PLAN R0 item 1), with
// the boot-race rule (bug report 2026-11-14): try the socket first; a
// live holder answers and this process stays a dialer. No live holder
// → attempt to become the holder through the SAME daemon.Open path.
// Losing the store race is a normal outcome, not an error: the
// winner is mid-takeover and will serve the socket shortly, so the
// loser falls back to the dial path with a bounded wait. Only a
// non-lock open failure is fatal. The Badger store lock stays
// fail-closed for writes throughout — only the process lifecycle
// changed, never the single-writer rule. MCP-mode logging is
// stderr-only; zero stdout bytes except JSON-RPC in every path (the
// R0 lint test enforces).
func runShim(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if holderLive(cfg.UDSPath) {
		return dialShim(cfg)
	}
	// No live holder: race to become it (same path as --both).
	d, err := daemon.Open(cfg)
	if err == nil {
		defer func() { _ = d.Close() }()
		return holderServe(cfg, d)
	}
	if !storeLocked(err) {
		return err // a real open failure is loud and fatal
	}
	// Lost the race: another starter holds the store lock and is
	// becoming the holder. Wait, bounded, for its socket — a lock
	// conflict is evidence of a concurrent starter, never of a broken
	// disk. Diagnostics are stderr-only.
	log.Printf("shim: store locked by another starter; waiting to dial %s", cfg.UDSPath)
	if waitHolder(cfg.UDSPath, dialRetryWindow) {
		return dialShim(cfg)
	}
	return fmt.Errorf("shim: no holder answered at %s within %s (a process holds the store lock but never served)",
		cfg.UDSPath, dialRetryWindow)
}

// dialShim connects to the live holder and serves MCP over stdio.
func dialShim(cfg config.Config) error {
	c, err := goClient.Dial(cfg.UDSPath)
	if err != nil {
		return err
	}
	defer c.Close()
	return mcpshim.New(c).Serve(os.Stdin, os.Stdout)
}

// storeLocked reports whether err is the badger directory-lock
// conflict. Matched on badger's fixed message (pinned v4.9.6,
// dir_windows.go:85 / dir_unix.go:56); the wraps added by truth.Open
// and daemon.Open are string-preserving, so the substring survives.
func storeLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Another process is using this Badger database")
}

// waitHolder polls until a holder answers on sock (bounded — never
// forever, per the no-hang clause of the boot-race report).
func waitHolder(sock string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if holderLive(sock) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(dialRetryInterval)
	}
}

// holderLive probes the socket with one real round trip. Dialing
// alone is not proof (Q1 finding: a present-but-dead socket dials and
// fails per-call); a live holder ANSWERS the probe — ABSTAIN counts.
func holderLive(sock string) bool {
	if _, err := os.Stat(sock); err != nil {
		return false
	}
	c, err := goClient.Dial(sock)
	if err != nil {
		return false
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err = c.Recall(ctx, []string{"engram-holder-probe"}, 1, "system")
	return err == nil
}

// runBoth is the Q1 co-start rule: one process, one store open, socket
// plus stdio together. Ends on stdio EOF (host disconnect) or signal.
func runBoth(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	d, err := daemon.Open(cfg)
	if err != nil {
		return err
	}
	defer func() {
		_ = d.Close()
	}()
	return holderServe(cfg, d)
}

// holderServe runs the socket+stdio loop for an already-open daemon —
// shared verbatim by --both and the takeover winner in runShim.
func holderServe(cfg config.Config, d *daemon.Daemon) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- d.Serve(cfg.UDSPath)
	}()
	if err := waitSock(cfg.UDSPath, 10*time.Second); err != nil {
		d.Stop()
		return err
	}
	c, err := goClient.Dial(cfg.UDSPath)
	if err != nil {
		d.Stop()
		return err
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shimErr := make(chan error, 1)
	go func() {
		shimErr <- mcpshim.New(c).Serve(os.Stdin, os.Stdout)
	}()
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		d.Stop()
		return err
	case err := <-shimErr:
		if err != nil {
			d.Stop()
			return err
		}
	}
	d.Stop()
	return nil
}

// runTick runs one maintenance tick through the LIVE daemon's socket
// (Q2 dogfood trigger). It deliberately dials instead of opening the
// store: a second store open on the same DataDir would hit the Badger
// directory lock, and bypassing the server would skip its FR16
// threshold refresh. Single writer preserved by construction.
func runTick(configPath string, tick int64) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.UDSPath); err != nil {
		return fmt.Errorf("tick: no live socket at %s: %w", cfg.UDSPath, err)
	}
	c, err := goClient.Dial(cfg.UDSPath)
	if err != nil {
		return err
	}
	defer c.Close()
	cons, pruned, err := c.Consolidate(context.Background(), tick)
	if err != nil {
		return err
	}
	fmt.Printf("tick=%d consolidated=%d pruned=%d\n", tick, cons, pruned)
	return nil
}

// waitSock polls for the socket file (same discipline as the daemon
// test harness — bounded, never forever).
func waitSock(sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s never appeared", sock)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
