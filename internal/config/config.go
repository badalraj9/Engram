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
	DataDir          string
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
}

// Default returns the v1 starting values (SRS FR6 beta ~ 0.5, d = 2048).
func Default() Config {
	return Config{
		DataDir:          "./engram-data",
		UDSPath:          "./engram.sock",
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
	return nil
}
