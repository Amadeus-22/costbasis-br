package domain

import (
	"fmt"
	"time"
)

// Position is the taxpayer's holding of one asset under the weighted average
// cost method: a quantity and the total BRL cost of that quantity.
type Position struct {
	Asset string
	Qty   Decimal
	Cost  Decimal // total acquisition cost in BRL, fees included
}

// AverageCost returns Cost / Qty, or zero for an empty position.
func (p Position) AverageCost() Decimal {
	if p.Qty.IsZero() {
		return Zero
	}
	return p.Cost.Quo(p.Qty)
}

// DisposalReason says why an asset left the taxpayer's hands.
type DisposalReason string

// Disposal reasons.
const (
	ReasonSale DisposalReason = "sale" // sold for fiat or traded for another asset
	ReasonFee  DisposalReason = "fee"  // used to pay a trading or stand-alone fee
)

// Disposal is one taxable alienation: Qty of Asset left the portfolio for
// Proceeds (BRL); Cost is the share of the position's cost it carried away.
type Disposal struct {
	TxID     string
	TxLabel  string
	Time     time.Time
	Exchange string
	Asset    string
	Qty      Decimal
	Proceeds Decimal
	Cost     Decimal
	Reason   DisposalReason
}

// Gain returns Proceeds - Cost (negative for a loss).
func (d Disposal) Gain() Decimal { return d.Proceeds.Sub(d.Cost) }

// OversellError reports a disposal larger than the position. It always means
// the imported history is incomplete (a missing buy, deposit or earlier
// export), so the message tells the user which transaction to look at.
type OversellError struct {
	TxLabel string
	Asset   string
	Held    Decimal
	Wanted  Decimal
}

func (e *OversellError) Error() string {
	return fmt.Sprintf("%s: disposes of %s %s but only %s %s is held; the history is incomplete (import the missing acquisitions or earlier exports)",
		e.TxLabel, e.Wanted, e.Asset, e.Held, e.Asset)
}

// Unwrap makes errors.Is(err, ErrInsufficientBalance) work.
func (e *OversellError) Unwrap() error { return ErrInsufficientBalance }
