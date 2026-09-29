package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"HOME": "/home/u"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != filepath.Join("/home/u", ".local", "share", "costbasis", "costbasis.db") {
		t.Errorf("DBPath = %s", c.DBPath)
	}
	if c.Ruleset != DefaultRuleset || c.HTTPAddr != DefaultHTTPAddr || c.LogLevel != slog.LevelInfo || c.RatesFile != "" {
		t.Errorf("defaults = %+v", c)
	}
	if c.Location.String() != DefaultTZ {
		t.Errorf("Location = %s", c.Location)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"XDG_DATA_HOME": "/data", "COSTBASIS_RATES_FILE": "r.csv", "COSTBASIS_RULESET": "x",
		"COSTBASIS_TZ": "UTC", "COSTBASIS_HTTP_ADDR": ":9000", "COSTBASIS_LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/data/costbasis/costbasis.db" || c.RatesFile != "r.csv" || c.Ruleset != "x" ||
		c.Location.String() != "UTC" || c.HTTPAddr != ":9000" || c.LogLevel != slog.LevelDebug {
		t.Errorf("config = %+v", c)
	}
	c, err = Load(env(map[string]string{"COSTBASIS_DB": "/tmp/x.db"}))
	if err != nil || c.DBPath != "/tmp/x.db" {
		t.Errorf("explicit DB: %+v %v", c, err)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"HOME": "/h", "COSTBASIS_TZ": "Mars/Olympus", "COSTBASIS_HTTP_ADDR": "nope", "COSTBASIS_LOG_LEVEL": "loud",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"COSTBASIS_TZ", "COSTBASIS_HTTP_ADDR", "COSTBASIS_LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
