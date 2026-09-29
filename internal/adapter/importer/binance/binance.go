// Package binance imports Binance's spot "Trade History" export
// (Orders → Spot Order → Trade History → Export).
//
// Binance does not publish the column layout. The layout below is taken from
// several independent open-source parsers (see README, "Binance export") and
// has not been checked against an export from a live account:
//
//	Date(UTC),Pair,Side,Price,Executed,Amount,Fee
//	2024-06-01 12:00:00,BTCUSDT,BUY,60000,0.001BTC,60USDT,0.01BNB
//
// Executed, Amount and Fee carry the asset as a suffix; Pair has no separator.
// The older layout "Date(UTC),Market,Type,Price,Amount,Total,Fee,Fee Coin"
// is also accepted. The export has no trade ID, so rows are identified by a
// fingerprint of their content (see domain.AssignIDs).
package binance

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

// Exchange is the exchange name stored on every imported transaction.
const Exchange = "binance"

const dateLayout = "2006-01-02 15:04:05"

var (
	headerCurrent = []string{"date(utc)", "pair", "side", "price", "executed", "amount", "fee"}
	headerLegacy  = []string{"date(utc)", "market", "type", "price", "amount", "total", "fee", "fee coin"}
)

// legacyQuotes are the quote assets recognised when splitting a legacy
// "Market" such as BTCUSDT. Longest first, so FDUSD wins over USD.
var legacyQuotes = []string{"FDUSD", "USDT", "USDC", "BUSD", "TUSD", "BRL", "EUR", "TRY", "BTC", "ETH", "BNB"}

// Importer parses Binance trade-history CSV exports.
type Importer struct{}

// Parse reads every row and stops at the first invalid one, naming its line.
func (Importer) Parse(ctx context.Context, r io.Reader) ([]domain.Transaction, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty file: header row expected")
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	var parse func([]string) (domain.Transaction, error)
	switch {
	case headerIs(header, headerCurrent):
		parse = parseCurrent
	case headerIs(header, headerLegacy):
		parse = parseLegacy
	default:
		return nil, fmt.Errorf("unrecognised Binance header %q; expected %q (or the legacy %q)",
			strings.Join(header, ","), "Date(UTC),Pair,Side,Price,Executed,Amount,Fee",
			"Date(UTC),Market,Type,Price,Amount,Total,Fee,Fee Coin")
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
		tx, err := parse(rec)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, tx)
	}
}

func headerIs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(got[i], "\ufeff")))
		if h != want[i] {
			return false
		}
	}
	return true
}

func parseTime(s string) (time.Time, error) {
	t, err := time.ParseInLocation(dateLayout, strings.TrimSpace(s), time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("column Date(UTC): want %q: %w", dateLayout, err)
	}
	return t, nil
}

func parseSide(s string) (domain.Kind, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "BUY":
		return domain.KindBuy, nil
	case "SELL":
		return domain.KindSell, nil
	}
	return "", fmt.Errorf("side %q is neither BUY nor SELL", s)
}

// parseCurrent handles Date(UTC),Pair,Side,Price,Executed,Amount,Fee.
func parseCurrent(rec []string) (domain.Transaction, error) {
	tx := domain.Transaction{Exchange: Exchange}
	var err error
	if tx.Time, err = parseTime(rec[0]); err != nil {
		return tx, err
	}
	if tx.Kind, err = parseSide(rec[2]); err != nil {
		return tx, err
	}
	pair := domain.NormaliseAsset(rec[1])
	base, quote, qty, amount, err := splitPair(pair, rec[4], rec[5])
	if err != nil {
		return tx, err
	}
	tx.Asset, tx.Qty, tx.QuoteAsset, tx.QuoteAmount = base, qty, quote, amount
	if fee := strings.TrimSpace(rec[6]); fee != "" {
		if tx.Fee, tx.FeeAsset, err = splitAmount(fee, base, quote); err != nil {
			return tx, fmt.Errorf("column Fee: %w", err)
		}
	}
	return tx, nil
}

// splitPair finds the base/quote split of a pair such as "BTCUSDT" for which
// Executed ends with the base and Amount ends with the quote. Exactly one
// split must fit; this also resolves symbols that start with digits, like
// 1INCH, without a hard-coded asset list.
func splitPair(pair, executed, amount string) (base, quote string, qty, total domain.Decimal, err error) {
	executed, amount = strings.ToUpper(strings.TrimSpace(executed)), strings.ToUpper(strings.TrimSpace(amount))
	found := 0
	for k := 1; k < len(pair); k++ {
		b, q := pair[:k], pair[k:]
		if !strings.HasSuffix(executed, b) || !strings.HasSuffix(amount, q) {
			continue
		}
		bq, err1 := parseNumber(strings.TrimSuffix(executed, b))
		qq, err2 := parseNumber(strings.TrimSuffix(amount, q))
		if err1 != nil || err2 != nil {
			continue
		}
		base, quote, qty, total = b, q, bq, qq
		found++
	}
	switch found {
	case 1:
		return base, quote, qty, total, nil
	case 0:
		return "", "", domain.Zero, domain.Zero, fmt.Errorf("cannot match Executed %q and Amount %q to pair %q", executed, amount, pair)
	default:
		return "", "", domain.Zero, domain.Zero, fmt.Errorf("ambiguous pair %q for Executed %q and Amount %q", pair, executed, amount)
	}
}

var leadingNumber = regexp.MustCompile(`^[0-9][0-9.,]*`)

// splitAmount splits "0.01BNB" into 0.01 and BNB, preferring the trade's own
// assets when the suffix is ambiguous (e.g. a fee in 1INCH).
func splitAmount(cell string, preferred ...string) (domain.Decimal, string, error) {
	cell = strings.ToUpper(strings.TrimSpace(cell))
	for _, asset := range preferred {
		if strings.HasSuffix(cell, asset) {
			if n, err := parseNumber(strings.TrimSuffix(cell, asset)); err == nil {
				return n, asset, nil
			}
		}
	}
	num := leadingNumber.FindString(cell)
	asset := cell[len(num):]
	if num == "" || asset == "" {
		return domain.Zero, "", fmt.Errorf("%q is not <number><asset>", cell)
	}
	n, err := parseNumber(num)
	if err != nil {
		return domain.Zero, "", err
	}
	return n, asset, nil
}

var thousands = regexp.MustCompile(`^[0-9]{1,3}(,[0-9]{3})+(\.[0-9]+)?$`)

// parseNumber accepts a plain decimal, or one with comma thousands separators
// in the strict 1,234,567.89 form. Anything else is rejected.
func parseNumber(s string) (domain.Decimal, error) {
	if thousands.MatchString(s) {
		s = strings.ReplaceAll(s, ",", "")
	}
	return domain.ParseDecimal(s)
}

// parseLegacy handles Date(UTC),Market,Type,Price,Amount,Total,Fee,Fee Coin.
func parseLegacy(rec []string) (domain.Transaction, error) {
	tx := domain.Transaction{Exchange: Exchange}
	var err error
	if tx.Time, err = parseTime(rec[0]); err != nil {
		return tx, err
	}
	if tx.Kind, err = parseSide(rec[2]); err != nil {
		return tx, err
	}
	market := domain.NormaliseAsset(strings.ReplaceAll(rec[1], "/", ""))
	for _, q := range legacyQuotes {
		if strings.HasSuffix(market, q) && len(market) > len(q) {
			tx.Asset, tx.QuoteAsset = strings.TrimSuffix(market, q), q
			break
		}
	}
	if tx.Asset == "" {
		return tx, fmt.Errorf("cannot split market %q into base and quote (known quotes: %v)", market, legacyQuotes)
	}
	if tx.Qty, err = parseNumber(strings.TrimSpace(rec[4])); err != nil {
		return tx, fmt.Errorf("column Amount: %w", err)
	}
	if tx.QuoteAmount, err = parseNumber(strings.TrimSpace(rec[5])); err != nil {
		return tx, fmt.Errorf("column Total: %w", err)
	}
	if fee := strings.TrimSpace(rec[6]); fee != "" {
		if tx.Fee, err = parseNumber(fee); err != nil {
			return tx, fmt.Errorf("column Fee: %w", err)
		}
		tx.FeeAsset = domain.NormaliseAsset(rec[7])
	}
	return tx, nil
}
