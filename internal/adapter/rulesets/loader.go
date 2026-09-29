// Package rulesets loads tax rulesets from YAML files (one file per ruleset,
// named <id>.yaml) and validates them. See rulesets/br-example.yaml for the
// schema and docs/adr/0002-rules-as-dated-data.md for why rules are data.
package rulesets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Loader reads rulesets from a file system (os.DirFS or an embed.FS).
type Loader struct {
	FS fs.FS
}

// Load reads, decodes and validates the ruleset with the given ID.
func (l Loader) Load(_ context.Context, id string) (tax.Ruleset, error) {
	if !validID.MatchString(id) {
		return tax.Ruleset{}, fmt.Errorf("%w: id %q (only lowercase letters, digits and dashes)", tax.ErrRulesetNotFound, id)
	}
	raw, err := fs.ReadFile(l.FS, id+".yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return tax.Ruleset{}, fmt.Errorf("%w: %q", tax.ErrRulesetNotFound, id)
	}
	if err != nil {
		return tax.Ruleset{}, fmt.Errorf("read ruleset %q: %w", id, err)
	}
	rs, err := Decode(raw)
	if err != nil {
		return tax.Ruleset{}, fmt.Errorf("ruleset %q: %w", id, err)
	}
	if rs.ID != id {
		return tax.Ruleset{}, fmt.Errorf("ruleset file %s.yaml declares id %q", id, rs.ID)
	}
	return rs, nil
}

// Decode parses YAML into a validated ruleset. Unknown fields are errors, so
// a typo cannot silently drop a rule.
func Decode(raw []byte) (tax.Ruleset, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return tax.Ruleset{}, fmt.Errorf("decode yaml: %w", err)
	}
	rs := f.toDomain()
	if err := rs.Validate(); err != nil {
		return tax.Ruleset{}, err
	}
	return rs, nil
}

// --- YAML schema ---

// decimalText keeps the literal text of a YAML scalar, so "0.175" is read as
// exactly 175/1000 and never passes through float64.
type decimalText struct{ domain.Decimal }

func (d *decimalText) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: expected a number", n.Line)
	}
	v, err := domain.ParseDecimal(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Decimal = v
	return nil
}

// dateText is a YYYY-MM-DD calendar date.
type dateText struct{ time.Time }

func (d *dateText) UnmarshalYAML(n *yaml.Node) error {
	t, err := time.Parse(time.DateOnly, n.Value)
	if err != nil {
		return fmt.Errorf("line %d: want YYYY-MM-DD: %w", n.Line, err)
	}
	d.Time = t
	return nil
}

type validity struct {
	EffectiveFrom dateText  `yaml:"effective_from"`
	EffectiveTo   *dateText `yaml:"effective_to"`
	Source        string    `yaml:"source"`
	Verified      bool      `yaml:"verified"`
}

func (v validity) toDomain() tax.Validity {
	out := tax.Validity{From: v.EffectiveFrom.Time, Source: v.Source, Verified: v.Verified}
	if v.EffectiveTo != nil {
		out.To = v.EffectiveTo.Time
	}
	return out
}

type file struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	Jurisdiction string `yaml:"jurisdiction"`
	Currency     string `yaml:"currency"`
	Verified     bool   `yaml:"verified"`
	Disclaimer   string `yaml:"disclaimer"`

	ForeignExchanges []struct {
		validity `yaml:",inline"`
		Names    []string `yaml:"names"`
	} `yaml:"foreign_exchanges"`

	MonthlyExemption []struct {
		validity   `yaml:",inline"`
		SalesLimit decimalText `yaml:"sales_limit"`
	} `yaml:"monthly_exemption"`

	CapitalGainRates []struct {
		validity `yaml:",inline"`
		Brackets []struct {
			UpTo *decimalText `yaml:"up_to"`
			Rate decimalText  `yaml:"rate"`
		} `yaml:"brackets"`
		OffsetLossesInMonth bool   `yaml:"offset_losses_in_month"`
		PaymentCode         string `yaml:"payment_code"`
		PaymentDue          string `yaml:"payment_due"`
	} `yaml:"capital_gain_rates"`

	ForeignRegime []struct {
		validity           `yaml:",inline"`
		Rate               decimalText `yaml:"rate"`
		OffsetLossesInYear bool        `yaml:"offset_losses_in_year"`
		PaymentDue         string      `yaml:"payment_due"`
	} `yaml:"foreign_regime"`

	Reporting []struct {
		validity  `yaml:",inline"`
		ID        string      `yaml:"id"`
		Title     string      `yaml:"title"`
		Kind      string      `yaml:"kind"`
		Scope     string      `yaml:"scope"`
		Threshold decimalText `yaml:"threshold"`
		Due       string      `yaml:"due"`
	} `yaml:"reporting"`
}

func (f file) toDomain() tax.Ruleset {
	rs := tax.Ruleset{
		ID: f.ID, Name: f.Name, Jurisdiction: f.Jurisdiction, Currency: f.Currency,
		Verified: f.Verified, Disclaimer: f.Disclaimer,
	}
	for _, x := range f.ForeignExchanges {
		rs.ForeignExchanges = append(rs.ForeignExchanges, tax.ForeignExchanges{Validity: x.toDomain(), Names: x.Names})
	}
	for _, x := range f.MonthlyExemption {
		rs.Exemptions = append(rs.Exemptions, tax.MonthlyExemption{Validity: x.toDomain(), SalesLimit: x.SalesLimit.Decimal})
	}
	for _, x := range f.CapitalGainRates {
		r := tax.CapitalGainRate{Validity: x.toDomain(), OffsetLossesInMonth: x.OffsetLossesInMonth,
			PaymentCode: x.PaymentCode, PaymentDue: x.PaymentDue}
		for _, b := range x.Brackets {
			br := tax.Bracket{Rate: b.Rate.Decimal}
			if b.UpTo != nil {
				br.UpTo = b.UpTo.Decimal
			}
			r.Brackets = append(r.Brackets, br)
		}
		rs.CapitalGainRates = append(rs.CapitalGainRates, r)
	}
	for _, x := range f.ForeignRegime {
		rs.ForeignRegimes = append(rs.ForeignRegimes, tax.ForeignRegime{Validity: x.toDomain(), Rate: x.Rate.Decimal,
			OffsetLossesInYear: x.OffsetLossesInYear, PaymentDue: x.PaymentDue})
	}
	for _, x := range f.Reporting {
		rs.Reporting = append(rs.Reporting, tax.ReportingRule{Validity: x.toDomain(), ID: x.ID, Title: x.Title,
			Kind: tax.ReportingKind(x.Kind), Scope: tax.Scope(x.Scope), Threshold: x.Threshold.Decimal, Due: x.Due})
	}
	return rs
}
