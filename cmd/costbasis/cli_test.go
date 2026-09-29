package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageErrors(t *testing.T) {
	env := testEnv(t)
	tests := []struct {
		args   []string
		code   int
		errSub string
	}{
		{nil, 2, "no command given"},
		{[]string{"frobnicate"}, 2, "unknown command"},
		{[]string{"import"}, 2, "at least one file"},
		{[]string{"import", "--nope", "x.csv"}, 2, "flag provided but not defined"},
		{[]string{"report"}, 2, "needs --year"},
		{[]string{"report", "--year", "2026", "--output", "xml"}, 2, "unknown output format"},
		{[]string{"positions", "--date", "31/12/2026"}, 2, "YYYY-MM-DD"},
		{[]string{"serve", "extra"}, 2, "no positional arguments"},
		{[]string{"import", "--format", "kraken", "x.csv"}, 1, "no such file"},
		{[]string{"report", "--year", "2026", "--ruleset", "absent"}, 1, "ruleset not found"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			_, stderr, code := runCLI(t, env, tt.args...)
			if code != tt.code || !strings.Contains(stderr, tt.errSub) {
				t.Fatalf("exit %d stderr %q, want %d containing %q", code, stderr, tt.code, tt.errSub)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	env := testEnv(t)
	if out, _, code := runCLI(t, env, "version"); code != 0 || !strings.HasPrefix(out, "costbasis ") {
		t.Errorf("version: %d %q", code, out)
	}
	if out, _, code := runCLI(t, env, "help"); code != 0 || !strings.Contains(out, "NOT TAX ADVICE") {
		t.Errorf("help: %d %q", code, out)
	}
}

func TestInvalidConfigurationFailsFast(t *testing.T) {
	env := testEnv(t)
	env["COSTBASIS_TZ"] = "Nowhere/Land"
	if _, stderr, code := runCLI(t, env, "report", "--year", "2026"); code != 1 || !strings.Contains(stderr, "COSTBASIS_TZ") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestImportFromStdinAndPositions(t *testing.T) {
	env := testEnv(t)
	csv := "time,exchange,kind,asset,qty,quote_asset,quote_amount\n2026-01-05T10:00:00-03:00,brx,buy,BTC,0.5,BRL,250000\n"
	_, stderr, code := runCLIWithStdin(t, env, csv, "import", "-")
	if code != 0 {
		t.Fatalf("import -: %d %s", code, stderr)
	}
	out, stderr, code := runCLI(t, env, "positions", "--date", "2026-01-31", "--output", "csv")
	if code != 0 {
		t.Fatalf("positions: %d %s", code, stderr)
	}
	want := "asset,qty,cost_brl,average_cost_brl\nBTC,0.5,250000.00,500000.00\n"
	if out != want {
		t.Errorf("positions = %q, want %q", out, want)
	}
	// Before the purchase there is nothing.
	out, _, _ = runCLI(t, env, "positions", "--date", "2026-01-04", "--output", "json")
	if !strings.Contains(out, `"positions": []`) {
		t.Errorf("positions before purchase = %s", out)
	}
}

func TestMissingRateIsReported(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(t.TempDir(), "t.csv")
	csv := "time,exchange,kind,asset,qty,quote_asset,quote_amount\n2026-01-05T10:00:00-03:00,brx,buy,BTC,0.5,USDT,50000\n"
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, env, "import", path); code != 0 {
		t.Fatal(stderr)
	}
	_, stderr, code := runCLI(t, env, "report", "--year", "2026")
	if code != 1 || !strings.Contains(stderr, "2026-01-05 USDT/BRL") || !strings.Contains(stderr, "COSTBASIS_RATES_FILE") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func runCLIWithStdin(t *testing.T, env map[string]string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut strings.Builder
	code := run(t.Context(), args, strings.NewReader(stdin), &out, &errOut, func(k string) string { return env[k] })
	return out.String(), errOut.String(), code
}
