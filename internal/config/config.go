// Package config holds Engram's tunable constants (TECH-STACK §2.10).
//
// Everything the SRS flags as fit-not-guessed (beta, thresholds,
// dimension, caps, rate limits) lives here so it can be recalibrated
// without a rebuild.
package config

import (
	"errors"
	"strings"
)

// Config is the validated daemon configuration.
type Config struct {
	DataDir string
	// UDSPath is the holder's socket. Load derives it when no source
	// (file or env) sets one: <DataDir>/engram.sock — so pinning
	// ENGRAM_DATA_DIR alone moves the socket with the store (STEP 0
	// ruling: the data dir derives the socket, never the CWD). An
	// explicit uds_path from any source always wins verbatim.
	UDSPath          string
	Beta             float64
	Dim              int
	WorkingCap       int
	AbstainThreshold float64
	TrustLow         float64
	TrustUser        float64
	TrustSystem      float64
	RateLimitPerSec  int
	CompactionGuard  bool
	// Operator surface (R0): the local panel. Port 0 = off (v1
	// default — today's behavior unchanged); a set port REQUIRES a
	// token (fail-closed: the panel never serves unauthenticated).
	PanelPort  int
	PanelToken string
}

// Default returns the v1 starting values (SRS FR6 beta ~ 0.5, d = 2048).
// UDSPath is left empty — Load derives it from DataDir after file and
// env have spoken; Default itself must not pre-empt that derivation.
func Default() Config {
	return Config{
		DataDir:          "./engram-data",
		UDSPath:          "",
		Beta:             0.5,
		Dim:              2048,
		WorkingCap:       64,
		AbstainThreshold: 0.5,
		TrustLow:         0.25,
		TrustUser:        1.0,
		TrustSystem:      1.5,
		RateLimitPerSec:  100,
		CompactionGuard:  true,
	}
}

// Validate rejects unusable configurations.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("config: data_dir is required")
	}
	if strings.TrimSpace(c.UDSPath) == "" {
		return errors.New("config: uds_path is required")
	}
	if c.Beta < 0 || c.Beta > 2 {
		return errors.New("config: beta must be in [0,2]")
	}
	if c.Dim <= 0 || c.Dim%64 != 0 {
		return errors.New("config: dim must be a positive multiple of 64")
	}
	if c.WorkingCap <= 0 {
		return errors.New("config: working_cap must be positive")
	}
	if c.AbstainThreshold < 0 {
		return errors.New("config: abstain_threshold must be >= 0 (FR6 score scale is unbounded)")
	}
	if c.TrustLow <= 0 || c.TrustUser <= 0 || c.TrustSystem <= 0 {
		return errors.New("config: trust weights must be positive")
	}
	if c.RateLimitPerSec <= 0 {
		return errors.New("config: rate_limit_per_sec must be positive")
	}
	if c.PanelPort < 0 || c.PanelPort > 65535 {
		return errors.New("config: panel_port must be 0 (off) or in [1,65535]")
	}
	if c.PanelPort != 0 && strings.TrimSpace(c.PanelToken) == "" {
		return errors.New("config: panel_port set requires panel_token (the panel never serves unauthenticated)")
	}
	return nil
}
