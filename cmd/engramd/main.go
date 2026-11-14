// Command engramd runs the Engram memory daemon (P6 — BUILD-RANKS E;
// Q1 adds the MCP modes).
//
// Three modes, one binary:
//
//	engramd               serve the UDS socket only (no-MCP users; stdout clean)
//	engramd --mcp         shim only: stdio MCP translator dialing a live
//	                      socket (opens no store — N agents share one daemon)
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
	"syscall"
	"time"

	goClient "engram/client/go"
	"engram/internal/config"
	"engram/internal/daemon"
	"engram/internal/mcpshim"
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

// runShim dials a live socket and translates stdio MCP (Q1 --mcp mode).
// Fail-fast when the socket file is absent (definitely dead); a
// present-but-dead socket fails per-call as isError (fail-closed).
func runShim(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.UDSPath); err != nil {
		return fmt.Errorf("shim: no live socket at %s: %w", cfg.UDSPath, err)
	}
	c, err := goClient.Dial(cfg.UDSPath)
	if err != nil {
		return err
	}
	defer c.Close()
	return mcpshim.New(c).Serve(os.Stdin, os.Stdout)
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
