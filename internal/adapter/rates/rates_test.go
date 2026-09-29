package rates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

func TestRateLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rates.csv")
	content := "date,pair,rate\n# PTAX-like rates\n2026-01-05,usdt/brl,5.4321\n2026-01-05,USD/BRL,5.41\n2026-01-05,USD/BRL,5.41\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d", s.Len())
	}
	ctx := context.Background()
	// Any time on that calendar day matches, in whatever location the caller uses.
	sp := time.FixedZone("BRT", -3*3600)
	r, err := s.Rate(ctx, "usdt", time.Date(2026, 1, 5, 23, 59, 0, 0, sp))
	if err != nil || r.Cmp(domain.MustDecimal("5.4321")) != 0 {
		t.Fatalf("Rate = %s, %v", r, err)
	}
	if r, err := s.Rate(ctx, "BRL", time.Now()); err != nil || r.Cmp(domain.NewDecimalFromInt(1)) != 0 {
		t.Fatalf("BRL rate = %s, %v", r, err)
	}
	_, err = s.Rate(ctx, "USDT", time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, domain.ErrRateNotFound) || !strings.Contains(err.Error(), "2026-01-06 USDT/BRL") || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing rate error = %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, in, errSub string }{
		{"header", "day,pair,rate\n", "header must be"},
		{"date", "date,pair,rate\n05/01/2026,USD/BRL,5\n", "line 2: date"},
		{"pair", "date,pair,rate\n2026-01-05,USD/EUR,5\n", "must look like ASSET/BRL"},
		{"rate", "date,pair,rate\n2026-01-05,USD/BRL,5,4\n", "wrong number of fields"},
		{"non-positive", "date,pair,rate\n2026-01-05,USD/BRL,0\n", "must be positive"},
		{"conflict", "date,pair,rate\n2026-01-05,USD/BRL,5\n2026-01-05,USD/BRL,6\n", "conflicting rates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.in), "test")
			if err == nil || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error = %v, want containing %q", err, tt.errSub)
			}
		})
	}
}

func TestEmptyAndMissingFile(t *testing.T) {
	_, err := Empty("no rates file configured").Rate(context.Background(), "USDT", time.Now())
	if !errors.Is(err, domain.ErrRateNotFound) || !strings.Contains(err.Error(), "no rates file configured") {
		t.Fatalf("error = %v", err)
	}
	if _, err := LoadFile(filepath.Join(t.TempDir(), "absent.csv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want ErrNotExist", err)
	}
}
