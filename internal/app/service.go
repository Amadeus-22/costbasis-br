package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/costbasis"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

// Errors callers can branch on (the HTTP API maps them to status codes).
var (
	ErrUnknownFormat   = errors.New("unknown import format")
	ErrInvalidImport   = errors.New("invalid import file")
	ErrInvalidArgument = errors.New("invalid argument")
)

// Service implements the use cases shared by the CLI and the HTTP API.
type Service struct {
	store     Store
	rates     RateSource
	rulesets  RulesetLoader
	importers map[string]Importer
	loc       *time.Location
	fiat      []string
	log       *slog.Logger
}

// Deps are the Service's collaborators.
type Deps struct {
	Store     Store
	Rates     RateSource
	Rulesets  RulesetLoader
	Importers map[string]Importer // keyed by format name, e.g. "generic"
	Location  *time.Location      // taxpayer's time zone: month and rate-date boundaries
	Logger    *slog.Logger
}

// NewService builds a Service. All dependencies are required.
func NewService(d Deps) (*Service, error) {
	if d.Store == nil || d.Rates == nil || d.Rulesets == nil || d.Location == nil || d.Logger == nil || len(d.Importers) == 0 {
		return nil, errors.New("new service: missing dependency")
	}
	return &Service{
		store: d.Store, rates: d.Rates, rulesets: d.Rulesets, importers: d.Importers,
		loc: d.Location, fiat: costbasis.DefaultFiat, log: d.Logger,
	}, nil
}

// Formats lists the registered import formats.
func (s *Service) Formats() []string {
	out := make([]string, 0, len(s.importers))
	for k := range s.importers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ImportResult summarises an import.
type ImportResult struct {
	Format     string `json:"format"`
	Parsed     int    `json:"parsed"`
	Inserted   int    `json:"inserted"`
	Duplicates int    `json:"duplicates"`
}

// Import parses r with the named importer and stores the transactions.
// Re-importing the same data is a no-op: IDs are deterministic.
func (s *Service) Import(ctx context.Context, format string, r io.Reader) (ImportResult, error) {
	imp, ok := s.importers[strings.ToLower(format)]
	if !ok {
		return ImportResult{}, fmt.Errorf("%w %q (known: %s)", ErrUnknownFormat, format, strings.Join(s.Formats(), ", "))
	}
	txs, err := imp.Parse(ctx, r)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %s: %w", ErrInvalidImport, format, err)
	}
	domain.AssignIDs(txs)
	for _, tx := range txs {
		if err := tx.Validate(); err != nil {
			return ImportResult{}, err
		}
	}
	inserted, err := s.store.SaveTransactions(ctx, txs)
	if err != nil {
		return ImportResult{}, fmt.Errorf("save transactions: %w", err)
	}
	res := ImportResult{Format: format, Parsed: len(txs), Inserted: inserted, Duplicates: len(txs) - inserted}
	s.log.InfoContext(ctx, "import finished", "format", format, "parsed", res.Parsed, "inserted", res.Inserted, "duplicates", res.Duplicates)
	return res, nil
}

// Report recomputes cost basis from the full history and evaluates year
// against the ruleset.
func (s *Service) Report(ctx context.Context, year int, rulesetID string) (tax.YearReport, error) {
	if year < 2000 || year > 9999 {
		return tax.YearReport{}, fmt.Errorf("%w: year %d", ErrInvalidArgument, year)
	}
	rs, err := s.rulesets.Load(ctx, rulesetID)
	if err != nil {
		return tax.YearReport{}, fmt.Errorf("load ruleset: %w", err)
	}
	end := time.Date(year+1, time.January, 1, 0, 0, 0, 0, s.loc)
	res, ops, err := s.Recompute(ctx, end)
	if err != nil {
		return tax.YearReport{}, err
	}
	rep, err := tax.Evaluate(rs, tax.Input{
		Year: year, Location: s.loc, Disposals: res.Disposals, Operations: ops, YearEnd: res.Positions,
	})
	if err != nil {
		return tax.YearReport{}, fmt.Errorf("evaluate %d: %w", year, err)
	}
	return rep, nil
}

// Ready reports whether the service's dependencies are reachable.
func (s *Service) Ready(ctx context.Context) error { return s.store.Ping(ctx) }

// Positions returns holdings as of the end of the given local date.
func (s *Service) Positions(ctx context.Context, date time.Time) ([]domain.Position, error) {
	y, m, d := date.Date()
	res, _, err := s.Recompute(ctx, time.Date(y, m, d+1, 0, 0, 0, 0, s.loc))
	if err != nil {
		return nil, err
	}
	return res.Positions, nil
}

// Recompute replays every stored transaction before until through the
// weighted-average engine. It also returns each trade's BRL value, which
// reporting thresholds need.
func (s *Service) Recompute(ctx context.Context, until time.Time) (costbasis.Result, []tax.Operation, error) {
	txs, err := s.store.Transactions(ctx, until)
	if err != nil {
		return costbasis.Result{}, nil, fmt.Errorf("load transactions: %w", err)
	}
	entries := make([]costbasis.Entry, 0, len(txs))
	var ops []tax.Operation
	for _, tx := range txs {
		e, err := s.value(ctx, tx)
		if err != nil {
			return costbasis.Result{}, nil, err
		}
		entries = append(entries, e)
		switch tx.Kind {
		case domain.KindBuy, domain.KindSell, domain.KindFee:
			ops = append(ops, tax.Operation{Time: tx.Time, Exchange: tx.Exchange, Value: e.QuoteValue})
		}
	}
	res, err := costbasis.Run(entries, s.fiat)
	if err != nil {
		return costbasis.Result{}, nil, fmt.Errorf("compute cost basis: %w", err)
	}
	return res, ops, nil
}

// value attaches the BRL amounts the engine needs to tx.
func (s *Service) value(ctx context.Context, tx domain.Transaction) (costbasis.Entry, error) {
	e := costbasis.Entry{Tx: tx}
	date := tx.Time.In(s.loc)
	brl := func(asset string, amount domain.Decimal) (domain.Decimal, error) {
		if asset == "BRL" || amount.IsZero() {
			return amount, nil
		}
		rate, err := s.rates.Rate(ctx, asset, date)
		if err != nil {
			return domain.Zero, fmt.Errorf("value %s: %w", tx.Label(), err)
		}
		return amount.Mul(rate), nil
	}
	var err error
	switch tx.Kind {
	case domain.KindBuy, domain.KindSell:
		if e.QuoteValue, err = brl(tx.QuoteAsset, tx.QuoteAmount); err != nil {
			return e, err
		}
	case domain.KindFee, domain.KindIncome:
		// Market value: the stated quote if the export has one, otherwise
		// the asset's own rate.
		if tx.QuoteAsset != "" && !tx.QuoteAmount.IsZero() {
			e.QuoteValue, err = brl(tx.QuoteAsset, tx.QuoteAmount)
		} else {
			e.QuoteValue, err = brl(tx.Asset, tx.Qty)
		}
		if err != nil {
			return e, err
		}
	default:
		// Own-wallet movements: the engine needs no BRL value, so no rate
		// lookups are made (and none can fail).
		return e, nil
	}
	if !tx.Fee.IsZero() {
		if e.FeeValue, err = brl(tx.FeeAsset, tx.Fee); err != nil {
			return e, err
		}
	}
	return e, nil
}
