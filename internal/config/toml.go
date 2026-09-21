package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Load reads path (TOML subset), applies environment overrides, and
// returns the validated configuration. An empty path loads defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		entries, err := parseTOML(path)
		if err != nil {
			return Config{}, err
		}
		if err := applyMap(&cfg, entries); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseTOML parses a flat subset of TOML: [section] headers and
// key = value entries with string/integer/float/boolean values.
// '#' starts a comment outside quoted strings. Anything else errors.
func parseTOML(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]string)
	section := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(cutComment(sc.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("config: line %d: bad section header", lineNo)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, fmt.Errorf("config: line %d: empty section", lineNo)
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("config: line %d: expected key = value", lineNo)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" || val == "" {
			return nil, fmt.Errorf("config: line %d: bad entry", lineNo)
		}
		if section != "" {
			key = section + "." + key
		}
		out[strings.ToLower(key)] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// cutComment strips a '#' comment that appears outside quoted strings.
func cutComment(s string) string {
	var b strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\' && inQuotes:
			b.WriteRune(r)
			escaped = true
		case r == '"':
			b.WriteRune(r)
			inQuotes = !inQuotes
		case r == '#' && !inQuotes:
			return b.String()
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unquote removes one pair of surrounding double quotes.
func unquote(v string) (string, bool) {
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		s, err := strconv.Unquote(v)
		if err != nil {
			return "", false
		}
		return s, true
	}
	return v, true
}

// baseKey drops any section prefix: "ranker.beta" -> "beta".
func baseKey(k string) string {
	if i := strings.LastIndex(k, "."); i >= 0 {
		return k[i+1:]
	}
	return k
}

// applyMap folds parsed entries into cfg. Unknown keys are rejected.
func applyMap(cfg *Config, entries map[string]string) error {
	for raw, val := range entries {
		switch baseKey(raw) {
		case "data_dir":
			s, ok := unquote(val)
			if !ok {
				return fmt.Errorf("config: bad string for %s", raw)
			}
			cfg.DataDir = s
		case "uds_path":
			s, ok := unquote(val)
			if !ok {
				return fmt.Errorf("config: bad string for %s", raw)
			}
			cfg.UDSPath = s
		case "beta":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return fmt.Errorf("config: bad float for %s", raw)
			}
			cfg.Beta = f
		case "dim":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("config: bad integer for %s", raw)
			}
			cfg.Dim = n
		case "working_cap":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("config: bad integer for %s", raw)
			}
			cfg.WorkingCap = n
		case "abstain_threshold":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return fmt.Errorf("config: bad float for %s", raw)
			}
			cfg.AbstainThreshold = f
		case "trust_low":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return fmt.Errorf("config: bad float for %s", raw)
			}
			cfg.TrustLow = f
		case "trust_user":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return fmt.Errorf("config: bad float for %s", raw)
			}
			cfg.TrustUser = f
		case "trust_system":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return fmt.Errorf("config: bad float for %s", raw)
			}
			cfg.TrustSystem = f
		case "rate_limit_per_sec":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("config: bad integer for %s", raw)
			}
			cfg.RateLimitPerSec = n
		case "compaction_guard":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return fmt.Errorf("config: bad boolean for %s", raw)
			}
			cfg.CompactionGuard = b
		default:
			return fmt.Errorf("config: unknown key %q", raw)
		}
	}
	return nil
}

// applyEnv overlays ENGRAM_* variables onto cfg.
func applyEnv(cfg *Config) error {
	set := func(key string, fn func(string) error) error {
		if v, ok := os.LookupEnv(key); ok {
			if err := fn(v); err != nil {
				return fmt.Errorf("config: bad %s: %w", key, err)
			}
		}
		return nil
	}
	float := func(dst *float64) func(string) error {
		return func(v string) error {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return err
			}
			*dst = f
			return nil
		}
	}
	integer := func(dst *int) func(string) error {
		return func(v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return err
			}
			*dst = n
			return nil
		}
	}
	checks := []error{
		set("ENGRAM_DATA_DIR", func(v string) error { cfg.DataDir = v; return nil }),
		set("ENGRAM_UDS_PATH", func(v string) error { cfg.UDSPath = v; return nil }),
		set("ENGRAM_BETA", float(&cfg.Beta)),
		set("ENGRAM_DIM", integer(&cfg.Dim)),
		set("ENGRAM_WORKING_CAP", integer(&cfg.WorkingCap)),
		set("ENGRAM_ABSTAIN_THRESHOLD", float(&cfg.AbstainThreshold)),
		set("ENGRAM_TRUST_LOW", float(&cfg.TrustLow)),
		set("ENGRAM_TRUST_USER", float(&cfg.TrustUser)),
		set("ENGRAM_TRUST_SYSTEM", float(&cfg.TrustSystem)),
		set("ENGRAM_RATE_LIMIT_PER_SEC", integer(&cfg.RateLimitPerSec)),
		set("ENGRAM_COMPACTION_GUARD", func(v string) error {
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return err
			}
			cfg.CompactionGuard = b
			return nil
		}),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}
