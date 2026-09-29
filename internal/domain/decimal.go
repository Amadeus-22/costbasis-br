package domain

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Decimal is an exact, immutable rational number used for every quantity and
// amount of money in the system. It wraps math/big.Rat so that no operation
// can alias or mutate another value by accident: each method returns a fresh
// Decimal. The zero value is 0.
//
// Rounding never happens inside the domain. It happens once, at the output
// edge, through StringFixed.
type Decimal struct {
	r *big.Rat
}

// ErrInvalidDecimal is returned when a string is not a plain decimal number.
var ErrInvalidDecimal = errors.New("invalid decimal")

// Zero is the additive identity.
var Zero = Decimal{}

func (d Decimal) rat() *big.Rat {
	if d.r == nil {
		return new(big.Rat)
	}
	return d.r
}

// NewDecimalFromInt returns the integer n as a Decimal.
func NewDecimalFromInt(n int64) Decimal {
	return Decimal{r: new(big.Rat).SetInt64(n)}
}

// ParseDecimal parses a plain decimal such as "12", "-0.5" or "1234.000001".
// It deliberately rejects exponents, fractions ("1/3"), thousands separators,
// "NaN" and empty input: exchange exports are data, and anything unusual must
// fail loudly instead of being silently reinterpreted.
func ParseDecimal(s string) (Decimal, error) {
	s = strings.TrimSpace(s)
	if !isPlainDecimal(s) {
		return Decimal{}, fmt.Errorf("%w: %q", ErrInvalidDecimal, s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return Decimal{}, fmt.Errorf("%w: %q", ErrInvalidDecimal, s)
	}
	return Decimal{r: r}, nil
}

// MustDecimal is ParseDecimal for constants in tests and examples; it panics
// on invalid input.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// ParseRat parses the exact storage form produced by RatString ("a" or "a/b").
func ParseRat(s string) (Decimal, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || strings.ContainsAny(s, "eE") {
		return Decimal{}, fmt.Errorf("%w: %q", ErrInvalidDecimal, s)
	}
	return Decimal{r: r}, nil
}

func isPlainDecimal(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	digits, dots := 0, 0
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			dots++
		default:
			return false
		}
	}
	return digits > 0 && dots <= 1
}

// Add returns d + o.
func (d Decimal) Add(o Decimal) Decimal { return Decimal{r: new(big.Rat).Add(d.rat(), o.rat())} }

// Sub returns d - o.
func (d Decimal) Sub(o Decimal) Decimal { return Decimal{r: new(big.Rat).Sub(d.rat(), o.rat())} }

// Mul returns d * o.
func (d Decimal) Mul(o Decimal) Decimal { return Decimal{r: new(big.Rat).Mul(d.rat(), o.rat())} }

// Quo returns d / o. It panics if o is zero, like integer division: callers
// in the domain check for zero first because a zero divisor is a logic error.
func (d Decimal) Quo(o Decimal) Decimal {
	if o.IsZero() {
		panic("domain: Decimal division by zero")
	}
	return Decimal{r: new(big.Rat).Quo(d.rat(), o.rat())}
}

// Neg returns -d.
func (d Decimal) Neg() Decimal { return Decimal{r: new(big.Rat).Neg(d.rat())} }

// Cmp compares d and o and returns -1, 0 or +1.
func (d Decimal) Cmp(o Decimal) int { return d.rat().Cmp(o.rat()) }

// Sign returns -1, 0 or +1.
func (d Decimal) Sign() int { return d.rat().Sign() }

// IsZero reports whether d == 0.
func (d Decimal) IsZero() bool { return d.Sign() == 0 }

// Max returns the larger of d and o.
func (d Decimal) Max(o Decimal) Decimal {
	if d.Cmp(o) >= 0 {
		return d
	}
	return o
}

// Min returns the smaller of d and o.
func (d Decimal) Min(o Decimal) Decimal {
	if d.Cmp(o) <= 0 {
		return d
	}
	return o
}

// RatString returns the exact value as "a" or "a/b". It is the storage form:
// ParseRat(d.RatString()) == d for every d.
func (d Decimal) RatString() string { return d.rat().RatString() }

// String returns the exact value if it has a finite decimal expansion, and
// the rational form "a/b" otherwise. Use StringFixed for display.
func (d Decimal) String() string {
	r := d.rat()
	if places, ok := finiteDecimalPlaces(r.Denom()); ok {
		return r.FloatString(places)
	}
	return r.RatString()
}

// finiteDecimalPlaces reports whether 1/den has a finite decimal expansion
// (den = 2^a * 5^b) and, if so, how many places it needs: max(a, b).
func finiteDecimalPlaces(den *big.Int) (int, bool) {
	n := new(big.Int).Set(den)
	two, five := big.NewInt(2), big.NewInt(5)
	var a, b int
	mod := new(big.Int)
	for {
		q, m := new(big.Int).QuoRem(n, two, mod)
		if m.Sign() != 0 {
			break
		}
		n, a = q, a+1
	}
	for {
		q, m := new(big.Int).QuoRem(n, five, mod)
		if m.Sign() != 0 {
			break
		}
		n, b = q, b+1
	}
	if n.Cmp(big.NewInt(1)) != 0 {
		return 0, false
	}
	return max(a, b), true
}

// StringFixed rounds d to the given number of decimal places using
// round-half-away-from-zero (the convention used for BRL amounts on tax
// forms) and formats it with exactly that many places.
func (d Decimal) StringFixed(places int) string {
	return d.Round(places).rat().FloatString(places)
}

// Round returns d rounded to the given number of decimal places, half away
// from zero.
func (d Decimal) Round(places int) Decimal {
	if places < 0 {
		panic("domain: negative decimal places")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	r := d.rat()
	// n = |num| * scale; q, rem = n / den
	num := new(big.Int).Abs(r.Num())
	num.Mul(num, scale)
	q, rem := new(big.Int).QuoRem(num, r.Denom(), new(big.Int))
	// Round up when 2*rem >= den.
	if rem.Lsh(rem, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if r.Sign() < 0 {
		q.Neg(q)
	}
	return Decimal{r: new(big.Rat).SetFrac(q, scale)}
}

// MarshalText encodes the exact value, so JSON never loses precision.
func (d Decimal) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText accepts both plain decimals and the "a/b" form.
func (d *Decimal) UnmarshalText(b []byte) error {
	v, err := ParseRat(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}
