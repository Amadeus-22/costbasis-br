package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Golden-file tests drive the real binary's code path (CLI → app → SQLite →
// engine → ruleset → renderer) over the cases in testdata/golden. Each case
// directory has:
//
//	inputs.txt           lines "<format> <file>" to import, and "rates <file>"
//	report-<year>.<ext>  expected `report --year <year>` output (json, csv or txt=table)
//	error-<year>.txt     expected stderr of a report that must fail
//
// Regenerate after an intended change with:
//
//	go test ./cmd/costbasis -run Golden -update
//
// and review the diff: it is the behaviour change.
var update = flag.Bool("update", false, "rewrite golden files with the current output")

var expectedName = regexp.MustCompile(`^(report|error)-(\d{4})\.(json|csv|txt)$`)

func TestGolden(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("testdata", "golden", "*"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no golden cases: %v", err)
	}
	for _, dir := range cases {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			env := testEnv(t)
			ratesFile := importInputs(t, dir, env)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			checked := 0
			for _, e := range entries {
				m := expectedName.FindStringSubmatch(e.Name())
				if m == nil {
					continue
				}
				checked++
				kind, year, ext := m[1], m[2], m[3]
				output := map[string]string{"json": "json", "csv": "csv", "txt": "table"}[ext]
				args := []string{"report", "--year", year, "--output", output}
				if ratesFile != "" {
					args = append(args, "--rates", ratesFile)
				}
				stdout, stderr, code := runCLI(t, env, args...)
				got := stdout
				if kind == "error" {
					if code == 0 {
						t.Fatalf("%s: expected failure, got success:\n%s", e.Name(), stdout)
					}
					got = stderr
				} else if code != 0 {
					t.Fatalf("%s: exit %d: %s", e.Name(), code, stderr)
				}
				compareGolden(t, filepath.Join(dir, e.Name()), got)
			}
			if checked == 0 {
				t.Fatal("case has no report-*/error-* files")
			}
		})
	}
}

// importInputs imports every input twice: the second import must add
// nothing (re-import is a no-op). It returns the rates file, if any.
func importInputs(t *testing.T, dir string, env map[string]string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "inputs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ratesFile string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		format, file, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("inputs.txt: bad line %q", line)
		}
		path := filepath.Join(dir, file)
		if format == "rates" {
			ratesFile = path
			continue
		}
		for round := 1; round <= 2; round++ {
			stdout, stderr, code := runCLI(t, env, "import", path, "--format", format)
			if code != 0 {
				t.Fatalf("import %s: exit %d: %s", path, code, stderr)
			}
			if round == 2 && !strings.Contains(stdout, " 0 new,") {
				t.Fatalf("re-import of %s was not a no-op: %s", path, stdout)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ratesFile
}

func testEnv(t *testing.T) map[string]string {
	return map[string]string{
		"COSTBASIS_DB": filepath.Join(t.TempDir(), "golden.db"),
		"COSTBASIS_TZ": "America/Sao_Paulo",
	}
}

func runCLI(t *testing.T, env map[string]string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(""), &out, &errOut, func(k string) string { return env[k] })
	return out.String(), errOut.String(), code
}

func compareGolden(t *testing.T, path, got string) {
	t.Helper()
	// Paths in error messages differ between machines; make them relative.
	if wd, err := os.Getwd(); err == nil {
		got = strings.ReplaceAll(got, wd+string(filepath.Separator), "")
	}
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the current output (run with -update if the change is intended)\n%s", path, diff(string(want), got))
	}
}

// diff shows the first differing line; enough to locate a golden mismatch.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want: %q\n  got:  %q", i+1, wl, gl)
		}
	}
	return "(no line differs)"
}
