// Package tax turns disposals and positions into a monthly report according
// to a Ruleset. Rulesets are data (see docs/adr/0002-rules-as-dated-data.md):
// every threshold, rate and reporting obligation carries an effective-date
// range, the legal source it came from and whether a human verified it.
package tax

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

// Validity is the effective-date range and provenance shared by every rule.
// From and To are calendar dates (midnight UTC); a zero To means open-ended.
type Validity struct {
	From     time.Time
	To       time.Time
	Source   string
	Verified bool
}

// Covers reports whether the calendar date of day falls inside the range.
func (v Validity) Covers(day time.Time) bool {
	d := civil(day)
	return !d.Before(v.From) && (v.To.IsZero() || !d.After(v.To))
}

func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// MonthlyExemption: a month whose total sales are at or below SalesLimit is
// exempt from capital-gains tax (Brazil: "bens de pequeno valor").
type MonthlyExemption struct {
	Validity
	SalesLimit domain.Decimal
}

// Bracket is one step of a progressive schedule. A zero UpTo means "no upper
// limit" and is only valid for the last bracket.
type Bracket struct {
	UpTo domain.Decimal
	Rate domain.Decimal
}

// CapitalGainRate is the monthly capital-gains regime for assets held in
// Brazil (or everywhere, before a ForeignRegime exists).
type CapitalGainRate struct {
	Validity
	Brackets            []Bracket // ascending, marginal ("parcela") rates
	OffsetLossesInMonth bool      // may a loss in one disposal reduce another's gain?
	PaymentCode         string    // e.g. DARF code
	PaymentDue          string    // human-readable deadline
}

// ForeignRegime taxes disposals on exchanges located abroad annually at a
// flat rate instead of monthly (Brazil: Lei 14.754/2023, as Receita reads it).
type ForeignRegime struct {
	Validity
	Rate               domain.Decimal
	OffsetLossesInYear bool
	PaymentDue         string
}

// ReportingKind selects how a reporting obligation is triggered.
type ReportingKind string

// Reporting kinds.
const (
	// MonthlyOperations triggers when the BRL value of the month's
	// operations in Scope exceeds Threshold.
	MonthlyOperations ReportingKind = "monthly_operations"
	// YearEndPosition triggers per asset whose year-end acquisition cost is
	// at or above Threshold.
	YearEndPosition ReportingKind = "year_end_position"
)

// Scope selects which exchanges a reporting rule looks at.
type Scope string

// Scopes.
const (
	ScopeAll     Scope = "all"
	ScopeForeign Scope = "foreign"
)

// ReportingRule is an obligation to inform the tax authority.
type ReportingRule struct {
	Validity
	ID        string
	Title     string
	Kind      ReportingKind
	Scope     Scope
	Threshold domain.Decimal
	Due       string
}

// ForeignExchanges lists exchanges treated as holding assets abroad.
type ForeignExchanges struct {
	Validity
	Names []string
}

// Ruleset is one jurisdiction's rules over time.
type Ruleset struct {
	ID               string
	Name             string
	Jurisdiction     string
	Currency         string
	Verified         bool
	Disclaimer       string
	ForeignExchanges []ForeignExchanges
	Exemptions       []MonthlyExemption
	CapitalGainRates []CapitalGainRate
	ForeignRegimes   []ForeignRegime
	Reporting        []ReportingRule
}

// Sentinel errors.
var (
	ErrInvalidRuleset  = errors.New("invalid ruleset")
	ErrRulesetNotFound = errors.New("ruleset not found")
)

// Validate checks structural invariants: sources present, date ranges sane,
// no overlapping ranges within one rule family, brackets ascending.
func (rs Ruleset) Validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w %q: %s", ErrInvalidRuleset, rs.ID, fmt.Sprintf(format, args...)))
	}
	if rs.ID == "" {
		fail("id is required")
	}
	if rs.Currency != "BRL" {
		fail("currency must be BRL, got %q", rs.Currency)
	}
	check := func(family string, vs []Validity) {
		sort.Slice(vs, func(i, j int) bool { return vs[i].From.Before(vs[j].From) })
		for i, v := range vs {
			if strings.TrimSpace(v.Source) == "" {
				fail("%s[%d]: source is required", family, i)
			}
			if v.From.IsZero() {
				fail("%s[%d]: effective_from is required", family, i)
			}
			if !v.To.IsZero() && v.To.Before(v.From) {
				fail("%s[%d]: effective_to before effective_from", family, i)
			}
			if i > 0 && (vs[i-1].To.IsZero() || !vs[i-1].To.Before(v.From)) {
				fail("%s: ranges overlap at %s", family, v.From.Format(time.DateOnly))
			}
		}
	}
	check("foreign_exchanges", validities(rs.ForeignExchanges))
	check("monthly_exemption", validities(rs.Exemptions))
	check("capital_gain_rates", validities(rs.CapitalGainRates))
	check("foreign_regime", validities(rs.ForeignRegimes))
	byID := map[string][]Validity{}
	for _, r := range rs.Reporting {
		byID[r.ID] = append(byID[r.ID], r.Validity)
		if r.ID == "" || r.Title == "" {
			fail("reporting rule needs id and title")
		}
		if r.Kind != MonthlyOperations && r.Kind != YearEndPosition {
			fail("reporting %q: unknown kind %q", r.ID, r.Kind)
		}
		if r.Scope != ScopeAll && r.Scope != ScopeForeign {
			fail("reporting %q: unknown scope %q", r.ID, r.Scope)
		}
		if r.Threshold.Sign() < 0 {
			fail("reporting %q: negative threshold", r.ID)
		}
	}
	for id, vs := range byID {
		check("reporting "+id, vs)
	}
	for i, r := range rs.CapitalGainRates {
		if len(r.Brackets) == 0 {
			fail("capital_gain_rates[%d]: no brackets", i)
		}
		for j, b := range r.Brackets {
			last := j == len(r.Brackets)-1
			if b.Rate.Sign() < 0 || b.Rate.Cmp(domain.NewDecimalFromInt(1)) > 0 {
				fail("capital_gain_rates[%d]: rate %s outside [0,1]", i, b.Rate)
			}
			if b.UpTo.IsZero() != last {
				fail("capital_gain_rates[%d]: only the last bracket may (and must) be unbounded", i)
			}
			if j > 0 && !last && b.UpTo.Cmp(r.Brackets[j-1].UpTo) <= 0 {
				fail("capital_gain_rates[%d]: brackets not ascending", i)
			}
		}
	}
	for i, r := range rs.ForeignRegimes {
		if r.Rate.Sign() < 0 || r.Rate.Cmp(domain.NewDecimalFromInt(1)) > 0 {
			fail("foreign_regime[%d]: rate %s outside [0,1]", i, r.Rate)
		}
	}
	return errors.Join(errs...)
}

type validator interface{ validity() Validity }

func (v Validity) validity() Validity { return v }

func validities[T validator](rules []T) []Validity {
	out := make([]Validity, len(rules))
	for i, r := range rules {
		out[i] = r.validity()
	}
	return out
}

// activeAt returns the rule in effect on day, if any.
func activeAt[T validator](rules []T, day time.Time) (T, bool) {
	for _, r := range rules {
		if r.validity().Covers(day) {
			return r, true
		}
	}
	var zero T
	return zero, false
}

// IsForeign reports whether exchange is treated as located abroad on day.
func (rs Ruleset) IsForeign(exchange string, day time.Time) bool {
	fe, ok := activeAt(rs.ForeignExchanges, day)
	if !ok {
		return false
	}
	for _, n := range fe.Names {
		if strings.EqualFold(n, exchange) {
			return true
		}
	}
	return false
}

// ProgressiveTax applies marginal brackets to amount.
func ProgressiveTax(amount domain.Decimal, brackets []Bracket) domain.Decimal {
	tax := domain.Zero
	lower := domain.Zero
	for _, b := range brackets {
		if amount.Cmp(lower) <= 0 {
			break
		}
		upper := amount
		if !b.UpTo.IsZero() {
			upper = amount.Min(b.UpTo)
		}
		tax = tax.Add(upper.Sub(lower).Mul(b.Rate))
		lower = b.UpTo
		if b.UpTo.IsZero() {
			break
		}
	}
	return tax
}
