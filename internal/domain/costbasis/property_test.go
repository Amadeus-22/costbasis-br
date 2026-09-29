package costbasis

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

// Property tests use a seeded generator so a failure is reproducible: the
// seed is in the subtest name.

const propertyRuns = 200

// randDecimal returns a positive decimal with up to 8 fractional digits,
// the precision exchanges use for coin quantities.
func randDecimal(r *rand.Rand, maxInt int64) domain.Decimal {
	units := r.Int64N(maxInt*100_000_000) + 1
	return domain.NewDecimalFromInt(units).Quo(domain.NewDecimalFromInt(100_000_000))
}

func randomBuys(r *rand.Rand, n int) ([]Entry, domain.Decimal, domain.Decimal) {
	var entries []Entry
	qty, cost := domain.Zero, domain.Zero
	for i := 0; i < n; i++ {
		q, c := randDecimal(r, 5), randDecimal(r, 500_000)
		fee := randDecimal(r, 100)
		e := Entry{
			Tx: domain.Transaction{
				ID: fmt.Sprintf("buy-%d", i), Time: t0.Add(time.Duration(i) * time.Minute), Exchange: "p",
				Kind: domain.KindBuy, Asset: "BTC", Qty: q, QuoteAsset: "BRL", QuoteAmount: c,
				Fee: fee, FeeAsset: "BRL",
			},
			QuoteValue: c, FeeValue: fee,
		}
		entries = append(entries, e)
		qty, cost = qty.Add(q), cost.Add(c).Add(fee)
	}
	return entries, qty, cost
}

// splitRandomly splits total into n positive parts that sum exactly to total.
func splitRandomly(r *rand.Rand, total domain.Decimal, n int) []domain.Decimal {
	parts := make([]domain.Decimal, 0, n)
	rest := total
	for i := 0; i < n-1; i++ {
		// A random fraction in (0, 1/2] of what is left.
		frac := domain.NewDecimalFromInt(r.Int64N(1000) + 1).Quo(domain.NewDecimalFromInt(2000))
		p := rest.Mul(frac)
		parts = append(parts, p)
		rest = rest.Sub(p)
	}
	return append(parts, rest)
}

func TestPropertyBuyThenSellAllLeavesNothing(t *testing.T) {
	for seed := uint64(1); seed <= propertyRuns; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 0))
			entries, qty, totalCost := randomBuys(r, 1+r.IntN(10))
			for i, q := range splitRandomly(r, qty, 1+r.IntN(6)) {
				entries = append(entries, Entry{
					Tx: domain.Transaction{
						ID: fmt.Sprintf("sell-%d", i), Time: t0.Add(time.Hour + time.Duration(i)*time.Minute), Exchange: "p",
						Kind: domain.KindSell, Asset: "BTC", Qty: q, QuoteAsset: "BRL", QuoteAmount: domain.NewDecimalFromInt(1),
					},
					QuoteValue: domain.NewDecimalFromInt(1),
				})
			}
			res, err := Run(entries, DefaultFiat)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(res.Positions) != 0 {
				t.Fatalf("positions left after selling everything: %+v", res.Positions)
			}
			// Every real paid for the coins left through some disposal.
			sold := domain.Zero
			for _, dp := range res.Disposals {
				sold = sold.Add(dp.Cost)
			}
			if sold.Cmp(totalCost) != 0 {
				t.Fatalf("cost of disposals %s != total cost %s", sold, totalCost)
			}
		})
	}
}

func TestPropertyTransfersConserveCost(t *testing.T) {
	for seed := uint64(1); seed <= propertyRuns; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 1))
			entries, qty, totalCost := randomBuys(r, 1+r.IntN(5))
			remaining := qty
			for i := 0; i < 1+r.IntN(8); i++ {
				// Move a random share of the holding out and back in, paying a
				// small network fee in the coin itself.
				moved := remaining.Mul(domain.NewDecimalFromInt(r.Int64N(100) + 1).Quo(domain.NewDecimalFromInt(100)))
				fee := moved.Quo(domain.NewDecimalFromInt(r.Int64N(1000) + 2))
				at := t0.Add(time.Hour + time.Duration(i)*time.Minute)
				entries = append(entries,
					Entry{Tx: domain.Transaction{ID: fmt.Sprintf("out-%d", i), Time: at, Exchange: "a", Kind: domain.KindTransferOut,
						Asset: "BTC", Qty: moved, Fee: fee, FeeAsset: "BTC"}},
					Entry{Tx: domain.Transaction{ID: fmt.Sprintf("in-%d", i), Time: at.Add(time.Second), Exchange: "b", Kind: domain.KindTransferIn,
						Asset: "BTC", Qty: moved.Sub(fee)}},
				)
				remaining = remaining.Sub(fee)
			}
			res, err := Run(entries, DefaultFiat)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(res.Disposals) != 0 {
				t.Fatalf("transfers created disposals: %+v", res.Disposals)
			}
			if len(res.Positions) != 1 {
				t.Fatalf("positions = %+v", res.Positions)
			}
			p := res.Positions[0]
			if p.Cost.Cmp(totalCost) != 0 {
				t.Fatalf("cost %s != %s after transfers", p.Cost, totalCost)
			}
			if p.Qty.Cmp(remaining) != 0 {
				t.Fatalf("qty %s != %s after transfers", p.Qty, remaining)
			}
		})
	}
}

func TestPropertyCostIsConservedAcrossPartialSales(t *testing.T) {
	// For any buys and any partial sales: remaining cost + cost of disposals
	// equals everything paid.
	for seed := uint64(1); seed <= propertyRuns; seed++ {
		r := rand.New(rand.NewPCG(seed, 2))
		entries, qty, totalCost := randomBuys(r, 1+r.IntN(8))
		parts := splitRandomly(r, qty, 2+r.IntN(5))
		for i, q := range parts[:len(parts)-1] { // keep the last part
			entries = append(entries, Entry{
				Tx: domain.Transaction{ID: fmt.Sprintf("s-%d", i), Time: t0.Add(time.Hour + time.Duration(i)*time.Minute), Exchange: "p",
					Kind: domain.KindSell, Asset: "BTC", Qty: q, QuoteAsset: "BRL", QuoteAmount: domain.NewDecimalFromInt(7)},
				QuoteValue: domain.NewDecimalFromInt(7),
			})
		}
		res, err := Run(entries, DefaultFiat)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		sum := res.Positions[0].Cost
		for _, dp := range res.Disposals {
			sum = sum.Add(dp.Cost)
		}
		if sum.Cmp(totalCost) != 0 {
			t.Fatalf("seed %d: cost not conserved: %s != %s", seed, sum, totalCost)
		}
		if res.Positions[0].Qty.Cmp(parts[len(parts)-1]) != 0 {
			t.Fatalf("seed %d: qty %s != %s", seed, res.Positions[0].Qty, parts[len(parts)-1])
		}
	}
}
