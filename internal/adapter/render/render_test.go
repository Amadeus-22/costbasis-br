package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

func TestRoundingHappensHere(t *testing.T) {
	third := domain.NewDecimalFromInt(1).Quo(domain.NewDecimalFromInt(3))
	tests := []struct{ got, want string }{
		{money(domain.MustDecimal("1041.225")), "1041.23"},
		{money(third), "0.33"},
		{qty(domain.MustDecimal("0.10000000")), "0.1"},
		{qty(domain.MustDecimal("2")), "2"},
		{qty(third), "0.33333333"},
		{qty(domain.MustDecimal("-0.000000001")), "0"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %s, want %s", tt.got, tt.want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"table", "JSON", "csv"} {
		if _, err := ParseFormat(s); err != nil {
			t.Errorf("ParseFormat(%q): %v", s, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("xml accepted")
	}
}

func sampleReport() tax.YearReport {
	return tax.YearReport{
		Year: 2026, RulesetID: "r", Disclaimer: "Check everything.",
		Months: []tax.MonthReport{{
			Month: time.March, Sales: domain.MustDecimal("40000"), Gain: domain.MustDecimal("1000"),
			ExemptionLimit: domain.MustDecimal("35000"), TaxableGain: domain.MustDecimal("1000"), EstimatedTax: domain.MustDecimal("150"),
			Assets: []tax.AssetLine{{Asset: "BTC", Regime: tax.RegimeDomestic, Disposals: 1, Qty: domain.MustDecimal("0.1"),
				Proceeds: domain.MustDecimal("40000"), Cost: domain.MustDecimal("39000"), Gain: domain.MustDecimal("1000")}},
			Flags: []tax.Flag{{RuleID: "pay", Title: "Pay", Detail: "d", Due: "soon"}},
		}},
		YearEnd:      []tax.PositionLine{{Asset: "BTC", Qty: domain.MustDecimal("1"), Cost: domain.MustDecimal("6000"), AverageCost: domain.MustDecimal("6000"), MustDeclare: true}},
		EstimatedTax: domain.MustDecimal("150"),
	}
}

func TestReportFormats(t *testing.T) {
	var table, js, csv bytes.Buffer
	if err := Report(&table, Table, sampleReport()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NOT TAX ADVICE", "UNVERIFIED", "2026-03", "150.00", "[unverified rule]", "yes"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}
	if err := Report(&js, JSON, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), `"estimated_tax": "150.00"`) || !strings.Contains(js.String(), `"must_declare": true`) {
		t.Errorf("json = %s", js.String())
	}
	if err := Report(&csv, CSV, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(csv.String()), "\n"); len(lines) != 2 || !strings.HasSuffix(lines[1], ",pay") {
		t.Errorf("csv = %q", csv.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, bytes.ErrTooLarge }

func TestWriteErrorsSurface(t *testing.T) {
	for _, f := range []Format{Table, JSON, CSV} {
		if err := Report(failingWriter{}, f, sampleReport()); err == nil {
			t.Errorf("%s: write error swallowed", f)
		}
	}
}
