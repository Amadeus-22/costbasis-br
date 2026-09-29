package costbasis

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

var d = domain.MustDecimal

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// entry builds a valued entry. Minutes after t0 keep the order explicit.
func entry(min int, kind domain.Kind, qty, asset, quoteAmt, quote, quoteBRL string) Entry {
	tx := domain.Transaction{
		ID: "t" + string(rune('a'+min)), Time: t0.Add(time.Duration(min) * time.Minute),
		Exchange: "test", Kind: kind, Asset: asset, Qty: d(qty), QuoteAsset: quote,
	}
	if quoteAmt != "" {
		tx.QuoteAmount = d(quoteAmt)
	}
	e := Entry{Tx: tx}
	if quoteBRL != "" {
		e.QuoteValue = d(quoteBRL)
	}
	return e
}

func withFee(e Entry, fee, asset, brl string) Entry {
	e.Tx.Fee, e.Tx.FeeAsset, e.FeeValue = d(fee), asset, d(brl)
	return e
}

type wantPos struct{ asset, qty, cost string }
type wantDisp struct {
	asset, qty, proceeds, cost string
	reason                     domain.DisposalReason
}

func TestEngine(t *testing.T) {
	tests := []struct {
		name      string
		entries   []Entry
		positions []wantPos
		disposals []wantDisp
	}{
		{
			name: "two buys average the cost",
			entries: []Entry{
				entry(0, domain.KindBuy, "1", "BTC", "100000", "BRL", "100000"),
				entry(1, domain.KindBuy, "1", "BTC", "200000", "BRL", "200000"),
			},
			positions: []wantPos{{"BTC", "2", "300000"}},
		},
		{
			name: "partial sell realises gain against average cost",
			entries: []Entry{
				entry(0, domain.KindBuy, "1", "BTC", "100000", "BRL", "100000"),
				entry(1, domain.KindBuy, "1", "BTC", "200000", "BRL", "200000"),
				entry(2, domain.KindSell, "0.5", "BTC", "90000", "BRL", "90000"),
			},
			positions: []wantPos{{"BTC", "1.5", "225000"}},
			disposals: []wantDisp{{"BTC", "0.5", "90000", "75000", domain.ReasonSale}},
		},
		{
			name: "buy fee in BRL raises cost, sell fee in BRL lowers proceeds",
			entries: []Entry{
				withFee(entry(0, domain.KindBuy, "2", "ETH", "20000", "BRL", "20000"), "50", "BRL", "50"),
				withFee(entry(1, domain.KindSell, "1", "ETH", "15000", "BRL", "15000"), "30", "BRL", "30"),
			},
			positions: []wantPos{{"ETH", "1", "10025"}},
			disposals: []wantDisp{{"ETH", "1", "14970", "10025", domain.ReasonSale}},
		},
		{
			name: "fee paid in the bought coin is a disposal at market value",
			entries: []Entry{
				withFee(entry(0, domain.KindBuy, "1", "BTC", "100000", "BRL", "100000"), "0.001", "BTC", "100"),
			},
			// Acquired 1 BTC at 100100; the 0.001 BTC fee leaves at its
			// market value (100) carrying 100.1 of cost. Remaining cost plus
			// the realised loss of 0.1 is exactly the 100000 BRL paid.
			positions: []wantPos{{"BTC", "0.999", "99999.9"}},
			disposals: []wantDisp{{"BTC", "0.001", "100", "100.1", domain.ReasonFee}},
		},
		{
			name: "swap into a stablecoin is a sale and an acquisition",
			entries: []Entry{
				entry(0, domain.KindBuy, "1", "BTC", "100000", "BRL", "100000"),
				entry(1, domain.KindSell, "1", "BTC", "25000", "USDT", "130000"),
			},
			positions: []wantPos{{"USDT", "25000", "130000"}},
			disposals: []wantDisp{{"BTC", "1", "130000", "100000", domain.ReasonSale}},
		},
		{
			name: "buying with a crypto quote disposes of the quote",
			entries: []Entry{
				entry(0, domain.KindBuy, "1000", "USDT", "5000", "BRL", "5000"),
				withFee(entry(1, domain.KindBuy, "2", "ETH", "600", "USDT", "3300"), "1", "USDT", "5.5"),
			},
			positions: []wantPos{{"ETH", "2", "3305.5"}, {"USDT", "399", "1995"}},
			disposals: []wantDisp{
				{"USDT", "600", "3300", "3000", domain.ReasonSale},
				{"USDT", "1", "5.5", "5", domain.ReasonFee},
			},
		},
		{
			name: "income is acquired at market value",
			entries: []Entry{
				entry(0, domain.KindIncome, "0.5", "SOL", "", "", "400"),
			},
			positions: []wantPos{{"SOL", "0.5", "400"}},
		},
		{
			name: "stand-alone fee is a disposal",
			entries: []Entry{
				entry(0, domain.KindBuy, "10", "BNB", "3000", "BRL", "3000"),
				entry(1, domain.KindFee, "1", "BNB", "", "", "350"),
			},
			positions: []wantPos{{"BNB", "9", "2700"}},
			disposals: []wantDisp{{"BNB", "1", "350", "300", domain.ReasonFee}},
		},
		{
			name: "transfer network fee keeps total cost",
			entries: []Entry{
				entry(0, domain.KindBuy, "1", "BTC", "100000", "BRL", "100000"),
				withFee(entry(1, domain.KindTransferOut, "0.5", "BTC", "", "", ""), "0.0001", "BTC", "0"),
				entry(2, domain.KindTransferIn, "0.4999", "BTC", "", "", ""),
			},
			positions: []wantPos{{"BTC", "0.9999", "100000"}},
		},
		{
			name: "fiat deposits and withdrawals are ignored",
			entries: []Entry{
				entry(0, domain.KindDeposit, "5000", "BRL", "", "", ""),
				withFee(entry(1, domain.KindWithdrawal, "1000", "BRL", "", "", ""), "5", "BRL", "5"),
			},
		},
		{
			name: "selling everything leaves no position",
			entries: []Entry{
				entry(0, domain.KindBuy, "3", "ETH", "10000", "BRL", "10000"),
				entry(1, domain.KindSell, "1", "ETH", "4000", "BRL", "4000"),
				entry(2, domain.KindSell, "2", "ETH", "7000", "BRL", "7000"),
			},
			disposals: []wantDisp{
				{"ETH", "1", "4000", "10000/3", domain.ReasonSale},
				{"ETH", "2", "7000", "20000/3", domain.ReasonSale},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Run(tt.entries, DefaultFiat)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(res.Positions) != len(tt.positions) {
				t.Fatalf("positions = %+v, want %d", res.Positions, len(tt.positions))
			}
			for i, w := range tt.positions {
				p := res.Positions[i]
				if p.Asset != w.asset || p.Qty.Cmp(ratOf(t, w.qty)) != 0 || p.Cost.Cmp(ratOf(t, w.cost)) != 0 {
					t.Errorf("position[%d] = %s %s cost %s, want %s %s cost %s", i, p.Qty, p.Asset, p.Cost, w.qty, w.asset, w.cost)
				}
			}
			if len(res.Disposals) != len(tt.disposals) {
				t.Fatalf("disposals = %d, want %d", len(res.Disposals), len(tt.disposals))
			}
			for i, w := range tt.disposals {
				got := res.Disposals[i]
				if got.Asset != w.asset || got.Qty.Cmp(ratOf(t, w.qty)) != 0 || got.Proceeds.Cmp(ratOf(t, w.proceeds)) != 0 ||
					got.Cost.Cmp(ratOf(t, w.cost)) != 0 || got.Reason != w.reason {
					t.Errorf("disposal[%d] = %s %s proceeds %s cost %s (%s), want %+v", i, got.Qty, got.Asset, got.Proceeds, got.Cost, got.Reason, w)
				}
			}
		})
	}
}

func ratOf(t *testing.T, s string) domain.Decimal {
	t.Helper()
	v, err := domain.ParseRat(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOversellNamesTheTransaction(t *testing.T) {
	buy := entry(0, domain.KindBuy, "1", "BTC", "100000", "BRL", "100000")
	sell := entry(1, domain.KindSell, "1.5", "BTC", "150000", "BRL", "150000")
	sell.Tx.ExternalID = "T-9001"
	sell.Tx.Exchange = "binance"

	_, err := Run([]Entry{buy, sell}, DefaultFiat)
	if !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("error = %v, want ErrInsufficientBalance", err)
	}
	var oe *domain.OversellError
	if !errors.As(err, &oe) {
		t.Fatalf("error %T is not *OversellError", err)
	}
	if oe.Asset != "BTC" || oe.Held.Cmp(d("1")) != 0 || oe.Wanted.Cmp(d("1.5")) != 0 {
		t.Errorf("OversellError = %+v", oe)
	}
	if !strings.Contains(err.Error(), "binance/T-9001") {
		t.Errorf("message %q does not name the transaction", err)
	}
}

func TestOversellOnTransferFee(t *testing.T) {
	out := withFee(entry(0, domain.KindWithdrawal, "1", "ETH", "", "", ""), "0.01", "ETH", "0")
	if _, err := Run([]Entry{out}, DefaultFiat); !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("error = %v, want ErrInsufficientBalance", err)
	}
}

func TestInvalidEntriesAreRejected(t *testing.T) {
	bad := entry(0, domain.KindBuy, "1", "BTC", "100", "BRL", "100")
	bad.Tx.Asset = ""
	if _, err := Run([]Entry{bad}, DefaultFiat); !errors.Is(err, domain.ErrInvalidTransaction) {
		t.Fatalf("error = %v, want ErrInvalidTransaction", err)
	}
	neg := entry(0, domain.KindBuy, "1", "BTC", "100", "BRL", "-100")
	if _, err := Run([]Entry{neg}, DefaultFiat); !errors.Is(err, domain.ErrInvalidTransaction) {
		t.Fatalf("error = %v, want ErrInvalidTransaction", err)
	}
}

func TestRunSortsByTimeAndKeepsTies(t *testing.T) {
	sell := entry(5, domain.KindSell, "1", "BTC", "10", "BRL", "10")
	buy := entry(1, domain.KindBuy, "1", "BTC", "5", "BRL", "5")
	if _, err := Run([]Entry{sell, buy}, DefaultFiat); err != nil {
		t.Fatalf("out-of-order input should be sorted: %v", err)
	}
	// Equal timestamps: caller order is kept, so sell-before-buy fails.
	buy.Tx.Time = sell.Tx.Time
	if _, err := Run([]Entry{sell, buy}, DefaultFiat); err == nil {
		t.Fatal("expected oversell when the sell comes first at the same instant")
	}
}
