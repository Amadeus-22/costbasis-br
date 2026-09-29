package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sample() []domain.Transaction {
	base := time.Date(2026, 3, 1, 12, 0, 0, 123456789, time.UTC)
	third := domain.NewDecimalFromInt(1).Quo(domain.NewDecimalFromInt(3))
	txs := []domain.Transaction{
		{Time: base.Add(time.Hour), Exchange: "x", Kind: domain.KindSell, Asset: "BTC", Qty: domain.MustDecimal("0.5"),
			QuoteAsset: "BRL", QuoteAmount: domain.MustDecimal("50000"), ExternalID: "S-1"},
		{Time: base, Exchange: "x", Kind: domain.KindBuy, Asset: "BTC", Qty: domain.MustDecimal("1"),
			QuoteAsset: "BRL", QuoteAmount: domain.MustDecimal("90000.12"), Fee: third, FeeAsset: "BRL"},
		// Same instant as the buy but inserted later: must come after it.
		{Time: base, Exchange: "x", Kind: domain.KindTransferOut, Asset: "BTC", Qty: domain.MustDecimal("0.1")},
	}
	domain.AssignIDs(txs)
	return txs
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "db.sqlite"))
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	in := sample()
	n, err := s.SaveTransactions(ctx, in)
	if err != nil || n != 3 {
		t.Fatalf("SaveTransactions = %d, %v", n, err)
	}
	out, err := s.Transactions(ctx, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("loaded %d", len(out))
	}
	// Ordered by time, then insertion.
	want := []domain.Transaction{in[1], in[2], in[0]}
	for i, w := range want {
		g := out[i]
		if g.ID != w.ID || !g.Time.Equal(w.Time) || g.Kind != w.Kind || g.Qty.Cmp(w.Qty) != 0 ||
			g.QuoteAmount.Cmp(w.QuoteAmount) != 0 || g.Fee.Cmp(w.Fee) != 0 || g.FeeAsset != w.FeeAsset || g.ExternalID != w.ExternalID {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
	}
	// Exact rationals survive storage.
	if out[0].Fee.String() != "1/3" {
		t.Errorf("fee = %s, want 1/3", out[0].Fee)
	}

	before, err := s.Transactions(ctx, in[0].Time)
	if err != nil || len(before) != 2 {
		t.Fatalf("until filter: %d, %v", len(before), err)
	}
}

func TestDatabaseFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	open(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("database mode = %o, want 600", perm)
	}
}

func TestReimportIsNoOp(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s := open(t, path)
	if _, err := s.SaveTransactions(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	n, err := s.SaveTransactions(ctx, sample())
	if err != nil || n != 0 {
		t.Fatalf("second save inserted %d, %v", n, err)
	}
	s.Close()

	// Reopening runs migrations again without harm and keeps the data.
	s2 := open(t, path)
	out, err := s2.Transactions(ctx, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(out) != 3 {
		t.Fatalf("after reopen: %d, %v", len(out), err)
	}
}

func TestSaveRejectsMissingIDAtomically(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "db.sqlite"))
	txs := sample()
	txs[2].ID = ""
	if _, err := s.SaveTransactions(ctx, txs); err == nil {
		t.Fatal("expected error")
	}
	out, err := s.Transactions(ctx, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(out) != 0 {
		t.Fatalf("partial write: %d rows, %v", len(out), err)
	}
}

func TestOpenFailsOnBadPath(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing-dir", "db.sqlite")); err == nil {
		t.Fatal("expected error")
	}
}
