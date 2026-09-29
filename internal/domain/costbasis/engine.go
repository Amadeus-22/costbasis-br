// Package costbasis implements the weighted average cost method (custo médio
// ponderado) used in Brazil for crypto assets. It is pure: no I/O, no clock,
// no floating point. Every BRL value it needs arrives pre-computed in Entry.
package costbasis

import (
	"fmt"
	"sort"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

// Entry is a transaction plus the BRL values the engine needs. The app layer
// fills them from a RateSource; the engine never looks up prices itself.
type Entry struct {
	Tx domain.Transaction
	// QuoteValue is the BRL value of the transaction's economic amount:
	// QuoteAmount for buy and sell, and the market value of Qty for fee and
	// income. Unused for deposits, withdrawals and transfers.
	QuoteValue domain.Decimal
	// FeeValue is the BRL value of Tx.Fee.
	FeeValue domain.Decimal
}

// DefaultFiat is the set of assets treated as money rather than holdings:
// spending or receiving them never creates a position or a disposal.
var DefaultFiat = []string{"BRL", "USD", "EUR", "GBP"}

// Engine replays transactions in order and keeps one weighted-average
// position per asset. The zero value is not usable; call New.
type Engine struct {
	fiat      map[string]bool
	positions map[string]*domain.Position
	disposals []domain.Disposal
}

// New returns an engine that treats the given assets as fiat.
func New(fiat []string) *Engine {
	f := make(map[string]bool, len(fiat))
	for _, a := range fiat {
		f[domain.NormaliseAsset(a)] = true
	}
	return &Engine{fiat: f, positions: make(map[string]*domain.Position)}
}

// Run is a convenience that applies entries in chronological order (stable
// for equal times, so the caller's order breaks ties) and returns the result.
func Run(entries []Entry, fiat []string) (Result, error) {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Tx.Time.Before(sorted[j].Tx.Time) })
	e := New(fiat)
	for _, en := range sorted {
		if err := e.Apply(en); err != nil {
			return Result{}, err
		}
	}
	return e.Result(), nil
}

// Result is the state after replaying a history.
type Result struct {
	Positions []domain.Position // non-empty positions, sorted by asset
	Disposals []domain.Disposal // in the order they happened
}

// Result returns a snapshot of the current positions and all disposals.
func (e *Engine) Result() Result {
	res := Result{Disposals: append([]domain.Disposal(nil), e.disposals...)}
	for _, p := range e.positions {
		if !p.Qty.IsZero() {
			res.Positions = append(res.Positions, *p)
		}
	}
	sort.Slice(res.Positions, func(i, j int) bool { return res.Positions[i].Asset < res.Positions[j].Asset })
	return res
}

// Apply processes one entry. Entries must be applied in chronological order.
//
// Rules (see docs/adr/0001-weighted-average-cost.md):
//   - buy:  acquire Qty at QuoteValue + FeeValue; if the quote is a crypto
//     asset, that asset is disposed of at QuoteValue (a swap is a sale).
//   - sell: dispose of Qty for QuoteValue - FeeValue; a crypto quote is
//     acquired at QuoteValue.
//   - a trading fee paid in a crypto asset is also a disposal of that asset
//     at its market value (FeeValue).
//   - income: acquire Qty at market value (QuoteValue).
//   - fee: dispose of Qty at market value (QuoteValue).
//   - deposit, withdrawal, transfer_in, transfer_out: own-wallet movements;
//     no taxable event. A network fee paid in crypto removes quantity but
//     keeps the cost, so total cost is conserved across transfers.
func (e *Engine) Apply(en Entry) error {
	tx := en.Tx
	if err := tx.Validate(); err != nil {
		return err
	}
	if en.QuoteValue.Sign() < 0 || en.FeeValue.Sign() < 0 {
		return fmt.Errorf("%w: %s: negative BRL value", domain.ErrInvalidTransaction, tx.Label())
	}
	switch tx.Kind {
	case domain.KindBuy:
		e.acquire(tx.Asset, tx.Qty, en.QuoteValue.Add(en.FeeValue))
		if err := e.dispose(tx, tx.QuoteAsset, tx.QuoteAmount, en.QuoteValue, domain.ReasonSale); err != nil {
			return err
		}
		return e.payTradingFee(en)
	case domain.KindSell:
		if err := e.dispose(tx, tx.Asset, tx.Qty, en.QuoteValue.Sub(en.FeeValue), domain.ReasonSale); err != nil {
			return err
		}
		e.acquire(tx.QuoteAsset, tx.QuoteAmount, en.QuoteValue)
		return e.payTradingFee(en)
	case domain.KindIncome:
		e.acquire(tx.Asset, tx.Qty, en.QuoteValue)
		return e.payTradingFee(en)
	case domain.KindFee:
		return e.dispose(tx, tx.Asset, tx.Qty, en.QuoteValue, domain.ReasonFee)
	case domain.KindDeposit, domain.KindTransferIn, domain.KindWithdrawal, domain.KindTransferOut:
		if tx.Fee.IsZero() {
			return nil
		}
		return e.removeKeepingCost(tx, tx.FeeAsset, tx.Fee)
	default:
		return fmt.Errorf("%w: %s: unhandled kind %q", domain.ErrInvalidTransaction, tx.Label(), tx.Kind)
	}
}

func (e *Engine) position(asset string) *domain.Position {
	p, ok := e.positions[asset]
	if !ok {
		p = &domain.Position{Asset: asset}
		e.positions[asset] = p
	}
	return p
}

func (e *Engine) acquire(asset string, qty, cost domain.Decimal) {
	if e.fiat[asset] {
		return
	}
	p := e.position(asset)
	p.Qty = p.Qty.Add(qty)
	p.Cost = p.Cost.Add(cost)
}

// dispose removes qty of asset at its average cost and records the disposal.
// The cost removed is qty/held * total cost, so selling everything removes
// exactly the total cost: no rounding residue can remain.
func (e *Engine) dispose(tx domain.Transaction, asset string, qty, proceeds domain.Decimal, reason domain.DisposalReason) error {
	if e.fiat[asset] {
		return nil
	}
	p := e.position(asset)
	if p.Qty.Cmp(qty) < 0 {
		return &domain.OversellError{TxLabel: tx.Label(), Asset: asset, Held: p.Qty, Wanted: qty}
	}
	cost := p.Cost.Mul(qty).Quo(p.Qty)
	p.Qty = p.Qty.Sub(qty)
	p.Cost = p.Cost.Sub(cost)
	e.disposals = append(e.disposals, domain.Disposal{
		TxID:     tx.ID,
		TxLabel:  tx.Label(),
		Time:     tx.Time,
		Exchange: tx.Exchange,
		Asset:    asset,
		Qty:      qty,
		Proceeds: proceeds,
		Cost:     cost,
		Reason:   reason,
	})
	return nil
}

// payTradingFee disposes of a fee paid in a crypto asset at its market value.
// Its BRL value has already been added to the acquisition cost (buy) or
// subtracted from the proceeds (sell).
func (e *Engine) payTradingFee(en Entry) error {
	tx := en.Tx
	if tx.Fee.IsZero() {
		return nil
	}
	return e.dispose(tx, tx.FeeAsset, tx.Fee, en.FeeValue, domain.ReasonFee)
}

// removeKeepingCost reduces a position's quantity without reducing its cost.
// Used for network fees on own-wallet transfers: the coins are gone, but what
// was paid for the whole position still stands, so the average cost rises.
func (e *Engine) removeKeepingCost(tx domain.Transaction, asset string, qty domain.Decimal) error {
	if e.fiat[asset] {
		return nil
	}
	p := e.position(asset)
	if p.Qty.Cmp(qty) < 0 {
		return &domain.OversellError{TxLabel: tx.Label(), Asset: asset, Held: p.Qty, Wanted: qty}
	}
	p.Qty = p.Qty.Sub(qty)
	if p.Qty.IsZero() {
		// Nothing left to carry the cost; it cannot be attributed to any
		// future sale.
		p.Cost = domain.Zero
	}
	return nil
}
