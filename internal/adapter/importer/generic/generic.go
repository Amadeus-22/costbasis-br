// Package generic imports the reference CSV format. Any exchange export can be
// converted to it with a spreadsheet, and it is the format the golden tests
// and examples use.
//
// Format (header row required, column order free, '#' starts a comment line):
//
//	time          RFC 3339 with offset, e.g. 2026-01-05T14:30:00-03:00   required
//	exchange      free text, e.g. binance                                 required
//	kind          buy|sell|fee|deposit|withdrawal|transfer_in|transfer_out|income
//	asset         symbol of the asset bought, sold, moved or received     required
//	qty           positive decimal, '.' separator, no thousands separator required
//	quote_asset   what was paid or received (BRL, USDT, ...)              buy/sell
//	quote_amount  amount of quote_asset                                   buy/sell
//	fee           fee amount, not already deducted from qty/quote_amount  optional
//	fee_asset     asset the fee was paid in                               with fee
//	external_id   the exchange's trade or transfer ID                     optional
package generic

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

// Columns lists the header names, in the canonical order.
var Columns = []string{"time", "exchange", "kind", "asset", "qty", "quote_asset", "quote_amount", "fee", "fee_asset", "external_id"}

var required = []string{"time", "exchange", "kind", "asset", "qty"}

// Importer parses the generic CSV format.
type Importer struct{}

// Parse reads every row. It stops at the first invalid row and names it.
func (Importer) Parse(ctx context.Context, r io.Reader) ([]domain.Transaction, error) {
	cr := csv.NewReader(r)
	cr.Comment = '#'
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty file: header row expected")
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if _, dup := col[name]; dup {
			return nil, fmt.Errorf("header: duplicate column %q", name)
		}
		col[name] = i
	}
	for _, name := range required {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("header: missing required column %q (have %v)", name, header)
		}
	}

	var out []domain.Transaction
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read csv: %w", err)
		}
		line, _ := cr.FieldPos(0)
		tx, err := parseRow(rec, col)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, tx)
	}
}

func parseRow(rec []string, col map[string]int) (domain.Transaction, error) {
	get := func(name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	dec := func(name string) (domain.Decimal, error) {
		v := get(name)
		if v == "" {
			return domain.Zero, nil
		}
		d, err := domain.ParseDecimal(v)
		if err != nil {
			return domain.Zero, fmt.Errorf("column %s: %w", name, err)
		}
		return d, nil
	}

	var tx domain.Transaction
	var err error
	if tx.Time, err = time.Parse(time.RFC3339, get("time")); err != nil {
		return tx, fmt.Errorf("column time: want RFC 3339 with offset (2026-01-05T14:30:00-03:00): %w", err)
	}
	tx.Time = tx.Time.UTC()
	tx.Exchange = strings.ToLower(get("exchange"))
	if tx.Kind, err = domain.ParseKind(get("kind")); err != nil {
		return tx, fmt.Errorf("column kind: %w", err)
	}
	tx.Asset = domain.NormaliseAsset(get("asset"))
	tx.QuoteAsset = domain.NormaliseAsset(get("quote_asset"))
	tx.FeeAsset = domain.NormaliseAsset(get("fee_asset"))
	tx.ExternalID = get("external_id")
	if tx.Qty, err = dec("qty"); err != nil {
		return tx, err
	}
	if tx.QuoteAmount, err = dec("quote_amount"); err != nil {
		return tx, err
	}
	if tx.Fee, err = dec("fee"); err != nil {
		return tx, err
	}
	return tx, nil
}
