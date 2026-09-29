package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseDecimal(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"0", "0", false},
		{"12", "12", false},
		{"-0.5", "-0.5", false},
		{"+3.25", "3.25", false},
		{" 1234.000001 ", "1234.000001", false},
		{".5", "0.5", false},
		{"5.", "5", false},
		{"", "", true},
		{"-", "", true},
		{".", "", true},
		{"1e5", "", true},
		{"1/3", "", true},
		{"1,000.00", "", true},
		{"1.2.3", "", true},
		{"NaN", "", true},
		{"0x10", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDecimal(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidDecimal) {
					t.Fatalf("ParseDecimal(%q) error = %v, want ErrInvalidDecimal", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDecimal(%q): %v", tt.in, err)
			}
			if got.String() != tt.want {
				t.Errorf("ParseDecimal(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecimalArithmeticIsExact(t *testing.T) {
	// 0.1 + 0.2 == 0.3 exactly, unlike float64.
	if got := MustDecimal("0.1").Add(MustDecimal("0.2")); got.Cmp(MustDecimal("0.3")) != 0 {
		t.Fatalf("0.1+0.2 = %s", got)
	}
	// (1/3) * 3 == 1 exactly.
	third := NewDecimalFromInt(1).Quo(NewDecimalFromInt(3))
	if got := third.Mul(NewDecimalFromInt(3)); got.Cmp(NewDecimalFromInt(1)) != 0 {
		t.Fatalf("(1/3)*3 = %s", got)
	}
	if third.String() != "1/3" {
		t.Errorf("String of 1/3 = %q, want exact rational form", third.String())
	}
}

func TestDecimalIsImmutable(t *testing.T) {
	a := MustDecimal("1.5")
	b := a.Add(MustDecimal("1"))
	_ = a.Neg()
	_ = a.Mul(b)
	if a.String() != "1.5" {
		t.Fatalf("a mutated to %s", a)
	}
	var zero Decimal
	if !zero.Add(zero).IsZero() {
		t.Fatal("zero value is not usable as 0")
	}
}

func TestRound(t *testing.T) {
	tests := []struct {
		in     string
		places int
		want   string
	}{
		{"1.005", 2, "1.01"},   // half away from zero
		{"-1.005", 2, "-1.01"}, // symmetric
		{"1.0049", 2, "1.00"},
		{"2.5", 0, "3"},
		{"-2.5", 0, "-3"},
		{"0.125", 2, "0.13"},
		{"123456789.123456789", 8, "123456789.12345679"},
		{"0", 2, "0.00"},
	}
	for _, tt := range tests {
		if got := MustDecimal(tt.in).StringFixed(tt.places); got != tt.want {
			t.Errorf("StringFixed(%s, %d) = %s, want %s", tt.in, tt.places, got, tt.want)
		}
	}
	third := NewDecimalFromInt(2).Quo(NewDecimalFromInt(3))
	if got := third.StringFixed(4); got != "0.6667" {
		t.Errorf("2/3 to 4 places = %s", got)
	}
}

func TestDecimalRoundTrip(t *testing.T) {
	for _, s := range []string{"0", "-7", "0.00000001", "1/3", "-22/7"} {
		d, err := ParseRat(s)
		if err != nil {
			t.Fatalf("ParseRat(%q): %v", s, err)
		}
		back, err := ParseRat(d.RatString())
		if err != nil || back.Cmp(d) != 0 {
			t.Errorf("round trip of %q failed: %v %s", s, err, back)
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var fromJSON Decimal
		if err := json.Unmarshal(raw, &fromJSON); err != nil || fromJSON.Cmp(d) != 0 {
			t.Errorf("JSON round trip of %q via %s failed: %v", s, raw, err)
		}
	}
	if _, err := ParseRat("1e3"); err == nil {
		t.Error("ParseRat accepted an exponent")
	}
}

func TestMinMax(t *testing.T) {
	a, b := MustDecimal("1"), MustDecimal("2")
	if a.Max(b).Cmp(b) != 0 || a.Min(b).Cmp(a) != 0 || b.Max(a).Cmp(b) != 0 || b.Min(a).Cmp(a) != 0 {
		t.Fatal("Min/Max wrong")
	}
}

func TestQuoByZeroPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewDecimalFromInt(1).Quo(Zero)
}
