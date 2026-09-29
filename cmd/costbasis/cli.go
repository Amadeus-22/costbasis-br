package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/adapter/httpapi"
	"github.com/Amadeus-22/costbasis-br/internal/adapter/importer/binance"
	"github.com/Amadeus-22/costbasis-br/internal/adapter/importer/generic"
	"github.com/Amadeus-22/costbasis-br/internal/adapter/rates"
	"github.com/Amadeus-22/costbasis-br/internal/adapter/render"
	"github.com/Amadeus-22/costbasis-br/internal/adapter/rulesets"
	"github.com/Amadeus-22/costbasis-br/internal/adapter/sqlite"
	"github.com/Amadeus-22/costbasis-br/internal/app"
	"github.com/Amadeus-22/costbasis-br/internal/platform/config"
	"github.com/Amadeus-22/costbasis-br/internal/platform/logging"
	shipped "github.com/Amadeus-22/costbasis-br/rulesets"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `costbasis: weighted-average cost basis and monthly crypto tax report (Brazil).
NOT TAX ADVICE: the bundled ruleset is an unverified example.

Usage:
  costbasis import [--format generic|binance] <file>...   import exchange exports ("-" reads stdin)
  costbasis report --year 2026 [--output table|json|csv] [--ruleset id] [--rates file]
  costbasis positions [--date 2026-12-31] [--output table|json|csv] [--rates file]
  costbasis serve [--rates file]                           local HTTP API on COSTBASIS_HTTP_ADDR
  costbasis version

Configuration is read from COSTBASIS_* environment variables; see the README.
`

// errUsage marks command-line mistakes (exit code 2).
var errUsage = errors.New("usage")

// run is main without globals, so tests can drive the whole program.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	err := dispatch(ctx, args, stdin, stdout, stderr, getenv)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "costbasis: %v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintf(stderr, "costbasis: %v\n", err)
		return 1
	}
}

func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: no command given", errUsage)
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "costbasis", version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "import", "report", "positions", "serve":
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	level := cfg.LogLevel
	if cmd != "serve" && getenv("COSTBASIS_LOG_LEVEL") == "" {
		level = slog.LevelWarn // keep one-shot commands quiet unless asked
	}
	log := logging.New(stderr, level)

	switch cmd {
	case "import":
		return cmdImport(ctx, cfg, log, args, stdin, stdout)
	case "report":
		return cmdReport(ctx, cfg, log, args, stdout)
	case "positions":
		return cmdPositions(ctx, cfg, log, args, stdout)
	default:
		return cmdServe(ctx, cfg, log, args)
	}
}

// parseFlags parses flags that may appear before or after positional
// arguments ("import file.csv --format binance"), unlike flag.Parse alone.
func parseFlags(fset *flag.FlagSet, args []string) ([]string, error) {
	fset.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fset.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %v", errUsage, err)
		}
		args = fset.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// openService wires adapters into the app service. The caller closes the
// returned store.
func openService(ctx context.Context, cfg config.Config, log *slog.Logger, ratesFile string) (*app.Service, *sqlite.Store, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create data directory: %w", err)
	}
	store, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	if ratesFile == "" {
		ratesFile = cfg.RatesFile
	}
	var rs app.RateSource = rates.Empty("a rates file (none configured: set COSTBASIS_RATES_FILE or pass --rates)")
	if ratesFile != "" {
		src, err := rates.LoadFile(ratesFile)
		if err != nil {
			store.Close()
			return nil, nil, err
		}
		rs = src
	}
	var rulesetFS fs.FS = shipped.FS
	if cfg.RulesetsDir != "" {
		rulesetFS = os.DirFS(cfg.RulesetsDir)
	}
	svc, err := app.NewService(app.Deps{
		Store:    store,
		Rates:    rs,
		Rulesets: rulesets.Loader{FS: rulesetFS},
		Importers: map[string]app.Importer{
			"generic": generic.Importer{},
			"binance": binance.Importer{},
		},
		Location: cfg.Location,
		Logger:   log,
	})
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return svc, store, nil
}

func cmdImport(ctx context.Context, cfg config.Config, log *slog.Logger, args []string, stdin io.Reader, stdout io.Writer) error {
	fset := flag.NewFlagSet("import", flag.ContinueOnError)
	format := fset.String("format", "generic", "export format: generic or binance")
	files, err := parseFlags(fset, args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%w: import needs at least one file", errUsage)
	}
	svc, store, err := openService(ctx, cfg, log, "")
	if err != nil {
		return err
	}
	defer store.Close()
	for _, name := range files {
		res, err := importFile(ctx, svc, *format, name, stdin)
		if err != nil {
			return fmt.Errorf("import %s: %w", name, err)
		}
		fmt.Fprintf(stdout, "%s: %d transactions read, %d new, %d already imported\n", name, res.Parsed, res.Inserted, res.Duplicates)
	}
	return nil
}

func importFile(ctx context.Context, svc *app.Service, format, name string, stdin io.Reader) (app.ImportResult, error) {
	if name == "-" {
		return svc.Import(ctx, format, stdin)
	}
	f, err := os.Open(name)
	if err != nil {
		return app.ImportResult{}, err
	}
	defer f.Close()
	return svc.Import(ctx, format, f)
}

func cmdReport(ctx context.Context, cfg config.Config, log *slog.Logger, args []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("report", flag.ContinueOnError)
	year := fset.Int("year", 0, "calendar year to report")
	output := fset.String("output", "table", "table, json or csv")
	ruleset := fset.String("ruleset", cfg.Ruleset, "ruleset id")
	ratesFile := fset.String("rates", "", "rates CSV (overrides COSTBASIS_RATES_FILE)")
	rest, err := parseFlags(fset, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 || *year == 0 {
		return fmt.Errorf("%w: report needs --year and no positional arguments", errUsage)
	}
	format, err := render.ParseFormat(*output)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	svc, store, err := openService(ctx, cfg, log, *ratesFile)
	if err != nil {
		return err
	}
	defer store.Close()
	rep, err := svc.Report(ctx, *year, *ruleset)
	if err != nil {
		return err
	}
	return render.Report(stdout, format, rep)
}

func cmdPositions(ctx context.Context, cfg config.Config, log *slog.Logger, args []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("positions", flag.ContinueOnError)
	dateStr := fset.String("date", time.Now().In(cfg.Location).Format(time.DateOnly), "positions at the end of this date")
	output := fset.String("output", "table", "table, json or csv")
	ratesFile := fset.String("rates", "", "rates CSV (overrides COSTBASIS_RATES_FILE)")
	if rest, err := parseFlags(fset, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return fmt.Errorf("%w: positions takes no positional arguments", errUsage)
	}
	date, err := time.ParseInLocation(time.DateOnly, *dateStr, cfg.Location)
	if err != nil {
		return fmt.Errorf("%w: --date must be YYYY-MM-DD", errUsage)
	}
	format, err := render.ParseFormat(*output)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	svc, store, err := openService(ctx, cfg, log, *ratesFile)
	if err != nil {
		return err
	}
	defer store.Close()
	ps, err := svc.Positions(ctx, date)
	if err != nil {
		return err
	}
	return render.Positions(stdout, format, date, ps)
}

func cmdServe(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) error {
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	ratesFile := fset.String("rates", "", "rates CSV (overrides COSTBASIS_RATES_FILE)")
	if rest, err := parseFlags(fset, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return fmt.Errorf("%w: serve takes no positional arguments", errUsage)
	}
	svc, store, err := openService(ctx, cfg, log, *ratesFile)
	if err != nil {
		return err
	}
	defer store.Close()
	api := httpapi.New(svc, cfg.Ruleset, cfg.Location, log)
	return httpapi.Serve(ctx, cfg.HTTPAddr, api.Handler(), log)
}
