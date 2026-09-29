package tax

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

var d = domain.MustDecimal

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func since(from string) Validity { return Validity{From: day(from), Source: "test"} }

func testRuleset() Ruleset {
	return Ruleset{
		ID: "test", Currency: "BRL",
		ForeignExchanges: []ForeignExchanges{{Validity: since("2000-01-01"), Names: []string{"Abroad"}}},
		Exemptions:       []MonthlyExemption{{Validity: since("2000-01-01"), SalesLimit: d("35000")}},
		CapitalGainRates: []CapitalGainRate{{
			Validity: since("2000-01-01"), PaymentCode: "4600",
			Brackets: []Bracket{
				{UpTo: d("5000000"), Rate: d("0.15")},
				{UpTo: d("10000000"), Rate: d("0.175")},
				{UpTo: d("30000000"), Rate: d("0.2")},
				{Rate: d("0.225")},
			},
		}},
		ForeignRegimes: []ForeignRegime{{Validity: since("2024-01-01"), Rate: d("0.15"), OffsetLossesInYear: true}},
		Reporting: []ReportingRule{
			{Validity: Validity{From: day("2019-08-01"), To: day("2026-06-30"), Source: "old"}, ID: "old", Title: "Old report",
				Kind: MonthlyOperations, Scope: ScopeForeign, Threshold: d("30000")},
			{Validity: since("2026-07-01"), ID: "new", Title: "New report", Kind: MonthlyOperations, Scope: ScopeForeign, Threshold: d("35000")},
			{Validity: since("2000-01-01"), ID: "annual", Title: "Annual", Kind: YearEndPosition, Scope: ScopeAll, Threshold: d("5000")},
		},
	}
}

func disposal(date, exchange, asset, proceeds, cost string) domain.Disposal {
	return domain.Disposal{Time: day(date).Add(15 * time.Hour), Exchange: exchange, Asset: asset, Qty: d("1"),
		Proceeds: d(proceeds), Cost: d(cost), Reason: domain.ReasonSale}
}

func TestValidateAcceptsTestRuleset(t *testing.T) {
	if err := testRuleset().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Ruleset)
		errSub string
	}{
		{"no id", func(r *Ruleset) { r.ID = "" }, "id is required"},
		{"currency", func(r *Ruleset) { r.Currency = "USD" }, "currency must be BRL"},
		{"missing source", func(r *Ruleset) { r.Exemptions[0].Source = "" }, "source is required"},
		{"overlap", func(r *Ruleset) {
			r.Exemptions = append(r.Exemptions, MonthlyExemption{Validity: since("2020-01-01"), SalesLimit: d("1")})
		}, "overlap"},
		{"inverted range", func(r *Ruleset) { r.Exemptions[0].To = day("1999-01-01") }, "effective_to before"},
		{"unbounded middle bracket", func(r *Ruleset) { r.CapitalGainRates[0].Brackets[1].UpTo = domain.Zero }, "unbounded"},
		{"descending brackets", func(r *Ruleset) { r.CapitalGainRates[0].Brackets[1].UpTo = d("1") }, "not ascending"},
		{"rate above one", func(r *Ruleset) { r.ForeignRegimes[0].Rate = d("1.5") }, "outside [0,1]"},
		{"reporting kind", func(r *Ruleset) { r.Reporting[0].Kind = "weekly" }, "unknown kind"},
		{"reporting scope", func(r *Ruleset) { r.Reporting[0].Scope = "mars" }, "unknown scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := testRuleset()
			tt.mutate(&rs)
			err := rs.Validate()
			if !errors.Is(err, ErrInvalidRuleset) || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.errSub)
			}
		})
	}
}

func TestProgressiveTax(t *testing.T) {
	br := testRuleset().CapitalGainRates[0].Brackets
	tests := []struct{ gain, want string }{
		{"0", "0"},
		{"1000", "150"},
		{"5000000", "750000"},
		// 750000 + 0.175 * 1000000
		{"6000000", "925000"},
		// 750000 + 875000 + 4000000 + 0.225 * 10000000
		{"40000000", "7875000"},
	}
	for _, tt := range tests {
		if got := ProgressiveTax(d(tt.gain), br); got.Cmp(d(tt.want)) != 0 {
			t.Errorf("ProgressiveTax(%s) = %s, want %s", tt.gain, got, tt.want)
		}
	}
}

func TestEvaluateMonthlyExemption(t *testing.T) {
	tests := []struct {
		name      string
		disposals []domain.Disposal
		exempt    bool
		taxable   string
		tax       string
	}{
		{"below limit is exempt", []domain.Disposal{disposal("2026-03-10", "br", "BTC", "34999.99", "20000")}, true, "0", "0"},
		{"exactly at limit is exempt", []domain.Disposal{disposal("2026-03-10", "br", "BTC", "35000", "20000")}, true, "0", "0"},
		{"above limit is taxed", []domain.Disposal{disposal("2026-03-10", "br", "BTC", "35000.01", "20000")}, false, "15000.01", "2250.0015"},
		{
			"limit is on total sales across assets, losses do not offset",
			[]domain.Disposal{
				disposal("2026-03-10", "br", "BTC", "20000", "10000"),
				disposal("2026-03-11", "br", "ETH", "20000", "25000"),
			}, false, "10000", "1500",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := Evaluate(testRuleset(), Input{Year: 2026, Disposals: tt.disposals})
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Months) != 1 {
				t.Fatalf("months = %d", len(rep.Months))
			}
			m := rep.Months[0]
			if m.Month != time.March || m.Exempt != tt.exempt || m.TaxableGain.Cmp(d(tt.taxable)) != 0 || m.EstimatedTax.Cmp(d(tt.tax)) != 0 {
				t.Errorf("month = exempt %v taxable %s tax %s, want %v %s %s", m.Exempt, m.TaxableGain, m.EstimatedTax, tt.exempt, tt.taxable, tt.tax)
			}
			if rep.EstimatedTax.Cmp(d(tt.tax)) != 0 {
				t.Errorf("year tax %s, want %s", rep.EstimatedTax, tt.tax)
			}
			hasPayment := false
			for _, f := range m.Flags {
				hasPayment = hasPayment || f.RuleID == "capital_gain_payment"
			}
			if hasPayment == tt.exempt {
				t.Errorf("payment flag present = %v for exempt = %v", hasPayment, tt.exempt)
			}
		})
	}
}

func TestEvaluateForeignRegime(t *testing.T) {
	in := Input{Year: 2026, Disposals: []domain.Disposal{
		disposal("2026-02-01", "abroad", "BTC", "100000", "60000"), // +40000, foreign
		disposal("2026-05-01", "abroad", "ETH", "10000", "15000"),  // -5000, foreign
		disposal("2026-05-02", "br", "SOL", "1000", "500"),         // domestic, exempt
	}}
	rep, err := Evaluate(testRuleset(), in)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Foreign == nil {
		t.Fatal("no foreign summary")
	}
	// Losses offset within the year: (40000 - 5000) * 15%.
	if rep.Foreign.TaxableGain.Cmp(d("35000")) != 0 || rep.Foreign.EstimatedTax.Cmp(d("5250")) != 0 {
		t.Errorf("foreign = %+v", rep.Foreign)
	}
	// Foreign sales do not count towards the domestic exemption.
	feb := rep.Months[0]
	if !feb.Sales.IsZero() || feb.ForeignSales.Cmp(d("100000")) != 0 || feb.EstimatedTax.Sign() != 0 {
		t.Errorf("february = sales %s foreign %s tax %s", feb.Sales, feb.ForeignSales, feb.EstimatedTax)
	}
	if rep.EstimatedTax.Cmp(d("5250")) != 0 {
		t.Errorf("year tax = %s", rep.EstimatedTax)
	}
}

func TestForeignRegimeOnlyAfterEffectiveDate(t *testing.T) {
	in := Input{Year: 2023, Disposals: []domain.Disposal{disposal("2023-02-01", "abroad", "BTC", "100000", "60000")}}
	rep, err := Evaluate(testRuleset(), in)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Foreign != nil {
		t.Fatal("foreign regime applied before it existed")
	}
	if rep.Months[0].EstimatedTax.Cmp(d("6000")) != 0 {
		t.Errorf("domestic tax = %s, want 6000", rep.Months[0].EstimatedTax)
	}
}

func TestReportingRulesFollowEffectiveDates(t *testing.T) {
	ops := []Operation{
		{Time: day("2026-06-10"), Exchange: "abroad", Value: d("32000")}, // > 30000 under the old rule
		{Time: day("2026-07-10"), Exchange: "abroad", Value: d("32000")}, // <= 35000 under the new rule
		{Time: day("2026-08-10"), Exchange: "abroad", Value: d("36000")}, // > 35000
		{Time: day("2026-09-10"), Exchange: "br", Value: d("99000")},     // domestic: out of scope
	}
	rep, err := Evaluate(testRuleset(), Input{Year: 2026, Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	want := map[time.Month]string{time.June: "old", time.July: "", time.August: "new", time.September: ""}
	for _, m := range rep.Months {
		got := ""
		for _, f := range m.Flags {
			got += f.RuleID
		}
		if got != want[m.Month] {
			t.Errorf("%s flags = %q, want %q", m.Month, got, want[m.Month])
		}
	}
}

func TestYearEndDeclaration(t *testing.T) {
	in := Input{Year: 2026, YearEnd: []domain.Position{
		{Asset: "BTC", Qty: d("0.1"), Cost: d("5000")},
		{Asset: "DOGE", Qty: d("100"), Cost: d("4999.99")},
	}}
	rep, err := Evaluate(testRuleset(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.YearEnd[0].MustDeclare || rep.YearEnd[1].MustDeclare {
		t.Errorf("year end = %+v", rep.YearEnd)
	}
	if len(rep.YearFlags) != 1 || !strings.Contains(rep.YearFlags[0].Detail, "BTC") {
		t.Errorf("year flags = %+v", rep.YearFlags)
	}
}

func TestMonthsFollowLocalTime(t *testing.T) {
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	// 2026-02-01 01:00 UTC is still 31 January in São Paulo.
	dp := disposal("2026-01-01", "br", "BTC", "1", "1")
	dp.Time = time.Date(2026, 2, 1, 1, 0, 0, 0, time.UTC)
	rep, err := Evaluate(testRuleset(), Input{Year: 2026, Location: sp, Disposals: []domain.Disposal{dp}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Months[0].Month != time.January {
		t.Errorf("month = %s, want January", rep.Months[0].Month)
	}
}

func TestMissingRatePeriodIsAnError(t *testing.T) {
	rs := testRuleset()
	rs.CapitalGainRates[0].From = day("2027-01-01")
	_, err := Evaluate(rs, Input{Year: 2026, Disposals: []domain.Disposal{disposal("2026-03-10", "br", "BTC", "1", "1")}})
	if !errors.Is(err, domain.ErrNoRulesetPeriod) {
		t.Fatalf("error = %v, want ErrNoRulesetPeriod", err)
	}
}

func TestIsForeignIsCaseInsensitive(t *testing.T) {
	rs := testRuleset()
	if !rs.IsForeign("ABROAD", day("2026-01-01")) || rs.IsForeign("br", day("2026-01-01")) {
		t.Fatal("IsForeign wrong")
	}
}
