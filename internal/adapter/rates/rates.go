// Package rates is a RateSource backed by a daily rates CSV:
//
//	date,pair,rate
//	2026-01-05,USDT/BRL,5.4321
//	2026-01-05,USD/BRL,5.4102
//
// One row per calendar date and pair, BRL as the quote currency. A missing
// rate is an error naming the date and pair; the source never falls back to
// a neighbouring day, because a guessed price is a wrong tax number.
package rates

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

type key struct {
	date  string // YYYY-MM-DD
	asset string
}

// Source holds every rate in memory. It is safe for concurrent reads.
type Source struct {
	origin string
	rates  map[key]domain.Decimal
}

// Empty returns a source with no rates; origin explains why, and appears in
// every "rate not found" error.
func Empty(origin string) *Source {
	return &Source{origin: origin, rates: map[key]domain.Decimal{}}
}

// LoadFile reads a rates CSV from disk.
func LoadFile(path string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open rates file: %w", err)
	}
	defer f.Close()
	s, err := Parse(f, path)
	if err != nil {
		return nil, fmt.Errorf("rates file %s: %w", path, err)
	}
	return s, nil
}

// Parse reads a rates CSV. origin names the data in error messages.
func Parse(r io.Reader, origin string) (*Source, error) {
	cr := csv.NewReader(r)
	cr.Comment = '#'
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = 3
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	for i, want := range []string{"date", "pair", "rate"} {
		if strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))) != want {
			return nil, fmt.Errorf("header must be date,pair,rate; got %q", strings.Join(header, ","))
		}
	}
	s := &Source{origin: origin, rates: map[key]domain.Decimal{}}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return s, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read csv: %w", err)
		}
		line, _ := cr.FieldPos(0)
		date, err := time.Parse(time.DateOnly, strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, fmt.Errorf("line %d: date: %w", line, err)
		}
		base, quote, ok := strings.Cut(domain.NormaliseAsset(rec[1]), "/")
		if !ok || base == "" || quote != "BRL" {
			return nil, fmt.Errorf("line %d: pair %q must look like ASSET/BRL", line, rec[1])
		}
		rate, err := domain.ParseDecimal(rec[2])
		if err != nil {
			return nil, fmt.Errorf("line %d: rate: %w", line, err)
		}
		if rate.Sign() <= 0 {
			return nil, fmt.Errorf("line %d: rate must be positive", line)
		}
		k := key{date.Format(time.DateOnly), base}
		if prev, dup := s.rates[k]; dup && prev.Cmp(rate) != 0 {
			return nil, fmt.Errorf("line %d: conflicting rates for %s %s/BRL: %s and %s", line, k.date, base, prev, rate)
		}
		s.rates[k] = rate
	}
}

// Rate returns the BRL price of one unit of asset on date's calendar day.
func (s *Source) Rate(_ context.Context, asset string, date time.Time) (domain.Decimal, error) {
	asset = domain.NormaliseAsset(asset)
	if asset == "BRL" {
		return domain.NewDecimalFromInt(1), nil
	}
	k := key{date.Format(time.DateOnly), asset}
	r, ok := s.rates[k]
	if !ok {
		return domain.Zero, fmt.Errorf("%w: %s %s/BRL (add it to %s)", domain.ErrRateNotFound, k.date, asset, s.origin)
	}
	return r, nil
}

// Len returns the number of rates loaded.
func (s *Source) Len() int { return len(s.rates) }
