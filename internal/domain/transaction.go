package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind is what a transaction does to the taxpayer's holdings.
type Kind string

// Transaction kinds. Their tax effect is defined by the costbasis engine and
// documented in docs/adr/0001-weighted-average-cost.md.
const (
	KindBuy         Kind = "buy"          // acquire Asset, pay QuoteAmount of QuoteAsset
	KindSell        Kind = "sell"         // dispose of Asset, receive QuoteAmount of QuoteAsset
	KindFee         Kind = "fee"          // stand-alone fee: Qty of Asset paid to the exchange
	KindDeposit     Kind = "deposit"      // money or coins arriving from outside the exchange
	KindWithdrawal  Kind = "withdrawal"   // money or coins leaving the exchange
	KindTransferIn  Kind = "transfer_in"  // coins arriving from the taxpayer's own wallet
	KindTransferOut Kind = "transfer_out" // coins leaving to the taxpayer's own wallet
	KindIncome      Kind = "income"       // staking, interest, airdrop: acquired at market value
)

// Kinds lists every valid Kind in a stable order.
var Kinds = []Kind{KindBuy, KindSell, KindFee, KindDeposit, KindWithdrawal, KindTransferIn, KindTransferOut, KindIncome}

// ParseKind validates s as a Kind.
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	for _, v := range Kinds {
		if k == v {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, s)
}

// Transaction is the normalised form every importer produces.
//
// Qty is always positive and denominated in Asset. QuoteAmount is the other
// leg of a trade (what was paid or received), in QuoteAsset. Fee is paid in
// FeeAsset and is never already netted out of Qty or QuoteAmount.
type Transaction struct {
	ID          string // deterministic fingerprint; see Fingerprint
	Time        time.Time
	Exchange    string
	Kind        Kind
	Asset       string
	Qty         Decimal
	QuoteAsset  string
	QuoteAmount Decimal
	Fee         Decimal
	FeeAsset    string
	ExternalID  string // exchange-provided trade or transfer ID, if any
}

// NormaliseAsset upper-cases and trims an asset symbol.
func NormaliseAsset(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// Validate checks the invariants every stored transaction must satisfy.
// The error names the transaction so users can find the offending row.
func (t Transaction) Validate() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalidTransaction, t.Label(), fmt.Sprintf(format, args...))
	}
	if t.Time.IsZero() {
		return fail("time is required")
	}
	if strings.TrimSpace(t.Exchange) == "" {
		return fail("exchange is required")
	}
	if _, err := ParseKind(string(t.Kind)); err != nil {
		return fail("unknown kind %q", t.Kind)
	}
	if t.Asset == "" {
		return fail("asset is required")
	}
	if t.Qty.Sign() < 0 || t.QuoteAmount.Sign() < 0 || t.Fee.Sign() < 0 {
		return fail("qty, quote_amount and fee must not be negative")
	}
	if t.Qty.IsZero() {
		return fail("qty must be positive")
	}
	switch t.Kind {
	case KindBuy, KindSell:
		if t.QuoteAsset == "" || t.QuoteAmount.IsZero() {
			return fail("%s needs quote_asset and a positive quote_amount", t.Kind)
		}
		if t.QuoteAsset == t.Asset {
			return fail("asset and quote_asset are both %s", t.Asset)
		}
	}
	if !t.Fee.IsZero() && t.FeeAsset == "" {
		return fail("fee_asset is required when fee is set")
	}
	return nil
}

// Label identifies the transaction in error messages: the exchange's own ID
// when available, otherwise a short fingerprint plus the time.
func (t Transaction) Label() string {
	id := t.ID
	if len(id) > 12 {
		id = id[:12]
	}
	if t.ExternalID != "" {
		return fmt.Sprintf("transaction %s/%s (%s %s at %s)", t.Exchange, t.ExternalID, t.Kind, t.Asset, t.Time.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("transaction %s (%s %s %s at %s)", id, t.Exchange, t.Kind, t.Asset, t.Time.UTC().Format(time.RFC3339))
}

// Fingerprint returns a deterministic ID so that importing the same file twice
// stores each transaction once. When the exchange provides an ID, that ID is
// the identity; otherwise every economic field is hashed, and occurrence
// distinguishes identical rows within one file (1 for the first, 2 for the
// second, ...).
func (t Transaction) Fingerprint(occurrence int) string {
	h := sha256.New()
	if t.ExternalID != "" {
		fmt.Fprintf(h, "ext\x00%s\x00%s", strings.ToLower(t.Exchange), t.ExternalID)
	} else {
		fmt.Fprintf(h, "row\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d",
			strings.ToLower(t.Exchange), t.Time.UTC().UnixNano(), t.Kind, t.Asset,
			t.Qty.RatString(), t.QuoteAsset, t.QuoteAmount.RatString(),
			t.Fee.RatString(), t.FeeAsset, occurrence)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AssignIDs sets ID on every transaction that lacks one, numbering identical
// rows by their order of appearance so the result is stable across imports.
func AssignIDs(txs []Transaction) {
	seen := make(map[string]int)
	for i := range txs {
		if txs[i].ID != "" {
			continue
		}
		base := txs[i].Fingerprint(0)
		seen[base]++
		txs[i].ID = txs[i].Fingerprint(seen[base])
	}
}

// Sentinel errors shared across layers.
var (
	ErrInvalidTransaction  = errors.New("invalid transaction")
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrRateNotFound        = errors.New("rate not found")
	ErrNoRulesetPeriod     = errors.New("no ruleset period covers date")
)
