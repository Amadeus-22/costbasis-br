package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validBuy() Transaction {
	return Transaction{
		Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Exchange: "x", Kind: KindBuy,
		Asset: "BTC", Qty: MustDecimal("1"), QuoteAsset: "BRL", QuoteAmount: MustDecimal("100"),
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Transaction)
		errSub string
	}{
		{"valid buy", func(*Transaction) {}, ""},
		{"missing time", func(t *Transaction) { t.Time = time.Time{} }, "time is required"},
		{"missing exchange", func(t *Transaction) { t.Exchange = " " }, "exchange is required"},
		{"bad kind", func(t *Transaction) { t.Kind = "gift" }, "unknown kind"},
		{"missing asset", func(t *Transaction) { t.Asset = "" }, "asset is required"},
		{"negative qty", func(t *Transaction) { t.Qty = MustDecimal("-1") }, "must not be negative"},
		{"zero qty", func(t *Transaction) { t.Qty = Zero }, "qty must be positive"},
		{"buy without quote", func(t *Transaction) { t.QuoteAsset = "" }, "needs quote_asset"},
		{"same asset both legs", func(t *Transaction) { t.QuoteAsset = "BTC" }, "both BTC"},
		{"fee without asset", func(t *Transaction) { t.Fee = MustDecimal("1") }, "fee_asset is required"},
		{"transfer without quote", func(t *Transaction) { t.Kind = KindTransferOut; t.QuoteAsset = ""; t.QuoteAmount = Zero }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := validBuy()
			tt.mutate(&tx)
			err := tx.Validate()
			if tt.errSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error = %v, want ErrInvalidTransaction containing %q", err, tt.errSub)
			}
		})
	}
}

func TestParseKind(t *testing.T) {
	for _, k := range Kinds {
		got, err := ParseKind(" " + strings.ToUpper(string(k)) + " ")
		if err != nil || got != k {
			t.Errorf("ParseKind(%q) = %q, %v", k, got, err)
		}
	}
	if _, err := ParseKind("trade"); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("ParseKind(trade) error = %v", err)
	}
}

func TestAssignIDsIsDeterministicAndDistinguishesDuplicates(t *testing.T) {
	mk := func() []Transaction { return []Transaction{validBuy(), validBuy(), validBuy()} }
	a, b := mk(), mk()
	AssignIDs(a)
	AssignIDs(b)
	seen := map[string]bool{}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("row %d: IDs differ between runs", i)
		}
		if seen[a[i].ID] {
			t.Fatalf("row %d: identical rows got the same ID", i)
		}
		seen[a[i].ID] = true
	}

	withExt := validBuy()
	withExt.ExternalID = "T-1"
	other := withExt
	other.Qty = MustDecimal("2") // same exchange ID wins over content
	if withExt.Fingerprint(1) != other.Fingerprint(1) {
		t.Error("external ID should define identity")
	}
}

func TestLabelPrefersExternalID(t *testing.T) {
	tx := validBuy()
	tx.ID = "abcdef0123456789"
	if got := tx.Label(); !strings.Contains(got, "abcdef012345") {
		t.Errorf("Label() = %q, want short fingerprint", got)
	}
	tx.ExternalID = "T-42"
	if got := tx.Label(); !strings.Contains(got, "x/T-42") {
		t.Errorf("Label() = %q, want exchange/external ID", got)
	}
}

func TestPositionAverageCost(t *testing.T) {
	if !(Position{}).AverageCost().IsZero() {
		t.Error("empty position average cost must be zero")
	}
	p := Position{Asset: "BTC", Qty: MustDecimal("3"), Cost: MustDecimal("100")}
	if got := p.AverageCost(); got.Mul(NewDecimalFromInt(3)).Cmp(MustDecimal("100")) != 0 {
		t.Errorf("AverageCost = %s", got)
	}
}

func TestOversellErrorUnwraps(t *testing.T) {
	err := error(&OversellError{TxLabel: "transaction x/T-1", Asset: "BTC", Held: MustDecimal("1"), Wanted: MustDecimal("2")})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatal("OversellError must unwrap to ErrInsufficientBalance")
	}
	if !strings.Contains(err.Error(), "x/T-1") || !strings.Contains(err.Error(), "only 1 BTC") {
		t.Errorf("message = %q", err)
	}
}
