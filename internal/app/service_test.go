package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

// --- hand-written fakes of the ports ---

type fakeStore struct {
	txs []domain.Transaction
	ids map[string]bool
}

func (f *fakeStore) SaveTransactions(_ context.Context, txs []domain.Transaction) (int, error) {
	if f.ids == nil {
		f.ids = map[string]bool{}
	}
	n := 0
	for _, tx := range txs {
		if f.ids[tx.ID] {
			continue
		}
		f.ids[tx.ID] = true
		f.txs = append(f.txs, tx)
		n++
	}
	return n, nil
}

func (f *fakeStore) Transactions(_ context.Context, until time.Time) ([]domain.Transaction, error) {
	var out []domain.Transaction
	for _, tx := range f.txs {
		if tx.Time.Before(until) {
			out = append(out, tx)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

type fakeRates map[string]string // "2026-01-05 USDT" -> "5.5"

func (f fakeRates) Rate(_ context.Context, asset string, date time.Time) (domain.Decimal, error) {
	key := date.Format(time.DateOnly) + " " + asset
	v, ok := f[key]
	if !ok {
		return domain.Zero, fmt.Errorf("%w: %s %s/BRL", domain.ErrRateNotFound, date.Format(time.DateOnly), asset)
	}
	return domain.MustDecimal(v), nil
}

type fakeRulesets struct{ rs tax.Ruleset }

func (f fakeRulesets) Load(_ context.Context, id string) (tax.Ruleset, error) {
	if id != f.rs.ID {
		return tax.Ruleset{}, fmt.Errorf("ruleset %q not found", id)
	}
	return f.rs, nil
}

// lineImporter parses "time kind qty asset quoteAmount quoteAsset" lines.
type lineImporter struct{}

func (lineImporter) Parse(_ context.Context, r io.Reader) ([]domain.Transaction, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var out []domain.Transaction
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		ts, err := time.Parse(time.RFC3339, f[0])
		if err != nil {
			return nil, err
		}
		tx := domain.Transaction{Time: ts, Exchange: "fake", Kind: domain.Kind(f[1]), Qty: domain.MustDecimal(f[2]), Asset: f[3]}
		if len(f) > 5 {
			tx.QuoteAmount, tx.QuoteAsset = domain.MustDecimal(f[4]), f[5]
		}
		out = append(out, tx)
	}
	return out, nil
}

func newTestService(t *testing.T, rates fakeRates) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{}
	rs := tax.Ruleset{
		ID: "test", Currency: "BRL",
		Exemptions: []tax.MonthlyExemption{{Validity: tax.Validity{From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Source: "t"}, SalesLimit: domain.MustDecimal("35000")}},
		CapitalGainRates: []tax.CapitalGainRate{{
			Validity: tax.Validity{From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Source: "t"},
			Brackets: []tax.Bracket{{Rate: domain.MustDecimal("0.15")}},
		}},
	}
	svc, err := NewService(Deps{
		Store: store, Rates: rates, Rulesets: fakeRulesets{rs},
		Importers: map[string]Importer{"lines": lineImporter{}},
		Location:  time.UTC, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

const history = `2026-01-05T10:00:00Z buy 1 BTC 100000 BRL
2026-02-10T10:00:00Z sell 0.5 BTC 10000 USDT
2026-02-11T10:00:00Z sell 4000 USDT 22000 BRL`

func TestImportIsIdempotent(t *testing.T) {
	svc, store := newTestService(t, nil)
	ctx := context.Background()
	first, err := svc.Import(ctx, "lines", strings.NewReader(history))
	if err != nil {
		t.Fatal(err)
	}
	if first.Parsed != 3 || first.Inserted != 3 || first.Duplicates != 0 {
		t.Fatalf("first import = %+v", first)
	}
	second, err := svc.Import(ctx, "LINES", strings.NewReader(history))
	if err != nil {
		t.Fatal(err)
	}
	if second.Inserted != 0 || second.Duplicates != 3 || len(store.txs) != 3 {
		t.Fatalf("second import = %+v, stored %d", second, len(store.txs))
	}
}

func TestImportRejectsUnknownFormatAndInvalidRows(t *testing.T) {
	svc, store := newTestService(t, nil)
	ctx := context.Background()
	if _, err := svc.Import(ctx, "kraken", strings.NewReader("")); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("error = %v, want ErrUnknownFormat", err)
	}
	bad := "2026-01-05T10:00:00Z buy 1 BTC 100000 BTC" // quote == asset
	if _, err := svc.Import(ctx, "lines", strings.NewReader(bad)); !errors.Is(err, domain.ErrInvalidTransaction) {
		t.Fatalf("error = %v, want ErrInvalidTransaction", err)
	}
	if len(store.txs) != 0 {
		t.Fatal("an invalid import must store nothing")
	}
}

func TestReportValuesCryptoQuotesInBRL(t *testing.T) {
	svc, _ := newTestService(t, fakeRates{"2026-02-10 USDT": "5.5", "2026-02-11 USDT": "5.5"})
	ctx := context.Background()
	if _, err := svc.Import(ctx, "lines", strings.NewReader(history)); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Report(ctx, 2026, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Months) != 2 {
		t.Fatalf("months = %+v", rep.Months)
	}
	feb := rep.Months[1]
	// BTC: 0.5 sold for 10000 USDT = 55000 BRL, cost 50000 -> +5000.
	// USDT: 4000 sold for 22000 BRL, cost 22000 -> 0.
	// Sales 77000 > 35000, taxable 5000, tax 750.
	if feb.Sales.Cmp(domain.MustDecimal("77000")) != 0 || feb.EstimatedTax.Cmp(domain.MustDecimal("750")) != 0 {
		t.Errorf("february sales %s tax %s", feb.Sales, feb.EstimatedTax)
	}
	pos, err := svc.Positions(ctx, time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 2 || pos[0].Asset != "BTC" || pos[1].Qty.Cmp(domain.MustDecimal("6000")) != 0 {
		t.Errorf("positions = %+v", pos)
	}
}

func TestReportMissingRateNamesDateAndPair(t *testing.T) {
	svc, _ := newTestService(t, fakeRates{})
	ctx := context.Background()
	if _, err := svc.Import(ctx, "lines", strings.NewReader(history)); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Report(ctx, 2026, "test")
	if !errors.Is(err, domain.ErrRateNotFound) {
		t.Fatalf("error = %v, want ErrRateNotFound", err)
	}
	if !strings.Contains(err.Error(), "2026-02-10 USDT/BRL") {
		t.Errorf("error %q does not name date and pair", err)
	}
}

func TestReportPropagatesOversell(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	if _, err := svc.Import(ctx, "lines", strings.NewReader("2026-01-05T10:00:00Z sell 1 BTC 100000 BRL")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Report(ctx, 2026, "test"); !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("error = %v, want ErrInsufficientBalance", err)
	}
}

func TestReportValidatesInput(t *testing.T) {
	svc, _ := newTestService(t, nil)
	if _, err := svc.Report(context.Background(), 1999, "test"); err == nil {
		t.Error("year 1999 accepted")
	}
	if _, err := svc.Report(context.Background(), 2026, "nope"); err == nil {
		t.Error("unknown ruleset accepted")
	}
}

func TestNewServiceRequiresDependencies(t *testing.T) {
	if _, err := NewService(Deps{}); err == nil {
		t.Fatal("expected error")
	}
}
