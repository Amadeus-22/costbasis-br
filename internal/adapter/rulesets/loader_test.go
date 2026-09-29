package rulesets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
	shipped "github.com/Amadeus-22/costbasis-br/rulesets"
)

func TestShippedRulesetLoadsAndIsMarkedUnverified(t *testing.T) {
	rs, err := Loader{FS: shipped.FS}.Load(context.Background(), "br-example")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Verified {
		t.Error("the example ruleset must stay verified: false")
	}
	if !strings.Contains(rs.Disclaimer, "unverified") {
		t.Errorf("disclaimer = %q", rs.Disclaimer)
	}
	all := []tax.Validity{}
	for _, r := range rs.Exemptions {
		all = append(all, r.Validity)
	}
	for _, r := range rs.CapitalGainRates {
		all = append(all, r.Validity)
	}
	for _, r := range rs.ForeignRegimes {
		all = append(all, r.Validity)
	}
	for _, r := range rs.Reporting {
		all = append(all, r.Validity)
	}
	for _, v := range all {
		if v.Verified {
			t.Errorf("rule from %s is marked verified", v.From.Format(time.DateOnly))
		}
	}
	// Exact decimal parsing: 17.5% is exactly 7/40.
	if got := rs.CapitalGainRates[0].Brackets[1].Rate.RatString(); got != "7/40" {
		t.Errorf("rate = %s", got)
	}
}

const minimal = `
id: mini
currency: BRL
capital_gain_rates:
  - effective_from: 2020-01-01
    brackets: [{ rate: 0.15 }]
    source: test
`

func TestLoadErrors(t *testing.T) {
	fsys := fstest.MapFS{
		"mini.yaml":    {Data: []byte(minimal)},
		"wrongid.yaml": {Data: []byte(strings.Replace(minimal, "id: mini", "id: other", 1))},
		"typo.yaml":    {Data: []byte(minimal + "exemptoin: []\n")},
		"float.yaml":   {Data: []byte(strings.Replace(minimal, "rate: 0.15", "rate: 1.5e-1", 1))},
		"nosrc.yaml":   {Data: []byte(strings.Replace(minimal, "source: test", "source: ''", 1))},
		"date.yaml":    {Data: []byte(strings.Replace(minimal, "2020-01-01", "01/01/2020", 1))},
	}
	l := Loader{FS: fsys}
	if _, err := l.Load(context.Background(), "mini"); err != nil {
		t.Fatalf("minimal ruleset: %v", err)
	}
	tests := []struct{ id, errSub string }{
		{"absent", "ruleset not found"},
		{"../etc", "only lowercase"},
		{"wrongid", "declares id"},
		{"typo", "not found in type"},
		{"float", "invalid decimal"},
		{"nosrc", "source is required"},
		{"date", "want YYYY-MM-DD"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			_, err := l.Load(context.Background(), tt.id)
			if err == nil || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error = %v, want containing %q", err, tt.errSub)
			}
		})
	}
	if _, err := l.Load(context.Background(), "absent"); !errors.Is(err, tax.ErrRulesetNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}
