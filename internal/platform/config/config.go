// Package config reads the process configuration from environment variables,
// once, at startup. Every variable is documented in the README.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the validated configuration.
type Config struct {
	DBPath      string         // COSTBASIS_DB
	RatesFile   string         // COSTBASIS_RATES_FILE (empty: none)
	Ruleset     string         // COSTBASIS_RULESET
	RulesetsDir string         // COSTBASIS_RULESETS_DIR (empty: embedded)
	Location    *time.Location // COSTBASIS_TZ
	HTTPAddr    string         // COSTBASIS_HTTP_ADDR
	LogLevel    slog.Level     // COSTBASIS_LOG_LEVEL
}

// Defaults.
const (
	DefaultRuleset  = "br-example"
	DefaultTZ       = "America/Sao_Paulo"
	DefaultHTTPAddr = "127.0.0.1:8080"
)

// Load reads the environment through getenv (os.Getenv in production, a map
// in tests) and validates it. All problems are reported at once.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	c := Config{
		DBPath:      getenv("COSTBASIS_DB"),
		RatesFile:   getenv("COSTBASIS_RATES_FILE"),
		Ruleset:     orDefault(getenv("COSTBASIS_RULESET"), DefaultRuleset),
		RulesetsDir: getenv("COSTBASIS_RULESETS_DIR"),
		HTTPAddr:    orDefault(getenv("COSTBASIS_HTTP_ADDR"), DefaultHTTPAddr),
	}
	if c.DBPath == "" {
		dir, err := dataDir(getenv)
		if err != nil {
			errs = append(errs, err)
		} else {
			c.DBPath = filepath.Join(dir, "costbasis", "costbasis.db")
		}
	}
	loc, err := time.LoadLocation(orDefault(getenv("COSTBASIS_TZ"), DefaultTZ))
	if err != nil {
		errs = append(errs, fmt.Errorf("COSTBASIS_TZ: %w", err))
	}
	c.Location = loc
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		errs = append(errs, fmt.Errorf("COSTBASIS_HTTP_ADDR %q: %w", c.HTTPAddr, err))
	}
	if err := c.LogLevel.UnmarshalText([]byte(orDefault(getenv("COSTBASIS_LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("COSTBASIS_LOG_LEVEL: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

// dataDir follows the XDG base-directory convention on every platform: the
// tool is a CLI, and one predictable location beats per-OS guessing.
func dataDir(getenv func(string) string) (string, error) {
	if d := getenv("XDG_DATA_HOME"); d != "" {
		return d, nil
	}
	home := getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("COSTBASIS_DB is unset and no home directory is known: %w", err)
		}
		home = h
	}
	return filepath.Join(home, ".local", "share"), nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
