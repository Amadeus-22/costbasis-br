// Package render is the output edge: it is the only place where exact values
// are rounded (BRL to 2 places, quantities to 8), and it writes reports as a
// table, JSON or CSV. The CLI and the HTTP API share it, so both print the
// same numbers.
package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

// Rounding at the output edge.
const (
	moneyPlaces = 2
	qtyPlaces   = 8
)

// Format is an output format.
type Format string

// Formats.
const (
	Table Format = "table"
	JSON  Format = "json"
	CSV   Format = "csv"
)

// ParseFormat validates s.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(s)); f {
	case Table, JSON, CSV:
		return f, nil
	}
	return "", fmt.Errorf("unknown output format %q (table, json, csv)", s)
}

func money(d domain.Decimal) string { return d.StringFixed(moneyPlaces) }

// qty rounds to 8 places and drops trailing zeros: 0.10000000 -> 0.1.
func qty(d domain.Decimal) string {
	s := d.StringFixed(qtyPlaces)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		return "0"
	}
	return s
}

// --- view model: rounded, JSON-ready ---

// ReportView is the rounded, serialisable form of a tax.YearReport.
type ReportView struct {
	Disclaimer   string         `json:"disclaimer"`
	Year         int            `json:"year"`
	Ruleset      RulesetView    `json:"ruleset"`
	Currency     string         `json:"currency"`
	Months       []MonthView    `json:"months"`
	Foreign      *ForeignView   `json:"foreign"`
	YearEnd      []PositionView `json:"year_end"`
	YearFlags    []FlagView     `json:"year_flags"`
	EstimatedTax string         `json:"estimated_tax"`
}

// RulesetView identifies the ruleset used.
type RulesetView struct {
	ID       string `json:"id"`
	Verified bool   `json:"verified"`
}

// MonthView is one month.
type MonthView struct {
	Month             string      `json:"month"`
	Sales             string      `json:"sales"`
	Gain              string      `json:"gain"`
	ExemptionLimit    string      `json:"exemption_limit"`
	Exempt            bool        `json:"exempt"`
	TaxableGain       string      `json:"taxable_gain"`
	EstimatedTax      string      `json:"estimated_tax"`
	ForeignSales      string      `json:"foreign_sales"`
	ForeignGain       string      `json:"foreign_gain"`
	Operations        string      `json:"operations"`
	ForeignOperations string      `json:"foreign_operations"`
	Assets            []AssetView `json:"assets"`
	Flags             []FlagView  `json:"flags"`
}

// AssetView is one asset's disposals in a month.
type AssetView struct {
	Asset     string `json:"asset"`
	Regime    string `json:"regime"`
	Disposals int    `json:"disposals"`
	Qty       string `json:"qty"`
	Proceeds  string `json:"proceeds"`
	Cost      string `json:"cost"`
	Gain      string `json:"gain"`
}

// FlagView is an obligation to check.
type FlagView struct {
	RuleID   string `json:"rule_id"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Due      string `json:"due"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
}

// ForeignView is the annual regime for assets held abroad.
type ForeignView struct {
	Gain         string   `json:"gain"`
	TaxableGain  string   `json:"taxable_gain"`
	Rate         string   `json:"rate"`
	EstimatedTax string   `json:"estimated_tax"`
	Flag         FlagView `json:"flag"`
}

// PositionView is a holding.
type PositionView struct {
	Asset       string `json:"asset"`
	Qty         string `json:"qty"`
	Cost        string `json:"cost"`
	AverageCost string `json:"average_cost"`
	MustDeclare *bool  `json:"must_declare,omitempty"`
}

func flagViews(fs []tax.Flag) []FlagView {
	out := make([]FlagView, 0, len(fs))
	for _, f := range fs {
		out = append(out, FlagView{RuleID: f.RuleID, Title: f.Title, Detail: f.Detail, Due: f.Due, Source: f.Source, Verified: f.Verified})
	}
	return out
}

// NewReportView rounds a report for output.
func NewReportView(r tax.YearReport) ReportView {
	v := ReportView{
		Disclaimer: r.Disclaimer, Year: r.Year, Currency: "BRL",
		Ruleset:      RulesetView{ID: r.RulesetID, Verified: r.RulesetVerified},
		Months:       []MonthView{},
		YearEnd:      []PositionView{},
		YearFlags:    flagViews(r.YearFlags),
		EstimatedTax: money(r.EstimatedTax),
	}
	for _, m := range r.Months {
		mv := MonthView{
			Month: fmt.Sprintf("%04d-%02d", r.Year, int(m.Month)), Sales: money(m.Sales), Gain: money(m.Gain),
			ExemptionLimit: money(m.ExemptionLimit), Exempt: m.Exempt, TaxableGain: money(m.TaxableGain),
			EstimatedTax: money(m.EstimatedTax), ForeignSales: money(m.ForeignSales), ForeignGain: money(m.ForeignGain),
			Operations: money(m.Operations), ForeignOperations: money(m.ForeignOperations),
			Assets: []AssetView{}, Flags: flagViews(m.Flags),
		}
		for _, a := range m.Assets {
			mv.Assets = append(mv.Assets, AssetView{Asset: a.Asset, Regime: string(a.Regime), Disposals: a.Disposals,
				Qty: qty(a.Qty), Proceeds: money(a.Proceeds), Cost: money(a.Cost), Gain: money(a.Gain)})
		}
		v.Months = append(v.Months, mv)
	}
	if f := r.Foreign; f != nil {
		v.Foreign = &ForeignView{Gain: money(f.Gain), TaxableGain: money(f.TaxableGain), Rate: f.Rate.String(),
			EstimatedTax: money(f.EstimatedTax), Flag: flagViews([]tax.Flag{f.Flag})[0]}
	}
	for _, p := range r.YearEnd {
		must := p.MustDeclare
		v.YearEnd = append(v.YearEnd, PositionView{Asset: p.Asset, Qty: qty(p.Qty), Cost: money(p.Cost),
			AverageCost: money(p.AverageCost), MustDeclare: &must})
	}
	return v
}

// NewPositionViews rounds positions for output.
func NewPositionViews(ps []domain.Position) []PositionView {
	out := make([]PositionView, 0, len(ps))
	for _, p := range ps {
		out = append(out, PositionView{Asset: p.Asset, Qty: qty(p.Qty), Cost: money(p.Cost), AverageCost: money(p.AverageCost())})
	}
	return out
}

// --- writers ---

// Report writes r in the given format.
func Report(w io.Writer, f Format, r tax.YearReport) error {
	v := NewReportView(r)
	switch f {
	case JSON:
		return writeJSON(w, v)
	case CSV:
		return reportCSV(w, v)
	default:
		return reportTable(w, v)
	}
}

// Positions writes holdings as of date in the given format.
func Positions(w io.Writer, f Format, date time.Time, ps []domain.Position) error {
	views := NewPositionViews(ps)
	switch f {
	case JSON:
		return writeJSON(w, struct {
			Date      string         `json:"date"`
			Positions []PositionView `json:"positions"`
		}{date.Format(time.DateOnly), views})
	case CSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"asset", "qty", "cost_brl", "average_cost_brl"})
		for _, p := range views {
			_ = cw.Write([]string{p.Asset, p.Qty, p.Cost, p.AverageCost})
		}
		cw.Flush()
		return cw.Error()
	default:
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintf(w, "Positions at end of %s (weighted average cost, BRL)\n\n", date.Format(time.DateOnly))
		fmt.Fprintln(tw, "ASSET\tQTY\tCOST\tAVG COST\t")
		for _, p := range views {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t\n", p.Asset, p.Qty, p.Cost, p.AverageCost)
		}
		return tw.Flush()
	}
}

// WriteJSON writes v as indented JSON; exported for the HTTP adapter.
func WriteJSON(w io.Writer, v any) error { return writeJSON(w, v) }

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

func reportCSV(w io.Writer, v ReportView) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"month", "sales_brl", "gain_brl", "exemption_limit_brl", "exempt", "taxable_gain_brl",
		"estimated_tax_brl", "foreign_sales_brl", "foreign_gain_brl", "flags"})
	for _, m := range v.Months {
		ids := make([]string, 0, len(m.Flags))
		for _, f := range m.Flags {
			ids = append(ids, f.RuleID)
		}
		_ = cw.Write([]string{m.Month, m.Sales, m.Gain, m.ExemptionLimit, strconv.FormatBool(m.Exempt), m.TaxableGain,
			m.EstimatedTax, m.ForeignSales, m.ForeignGain, strings.Join(ids, ";")})
	}
	cw.Flush()
	return cw.Error()
}

func verifiedMark(ok bool) string {
	if ok {
		return ""
	}
	return " [unverified rule]"
}

func reportTable(w io.Writer, v ReportView) error {
	ew := &errWriter{w: w}
	status := "UNVERIFIED"
	if v.Ruleset.Verified {
		status = "verified"
	}
	ew.printf("costbasis-br report %d, ruleset %s (%s)\n", v.Year, v.Ruleset.ID, status)
	ew.printf("NOT TAX ADVICE. %s\n\n", v.Disclaimer)

	if len(v.Months) == 0 {
		ew.printf("No disposals or trades in %d.\n", v.Year)
	} else {
		ew.printf("Monthly summary, BRL (domestic disposals are taxed monthly, foreign ones annually)\n")
		tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(tw, "MONTH\tSALES\tGAIN\tEXEMPT\tTAXABLE\tEST. TAX\tFOREIGN SALES\tFOREIGN GAIN\t")
		for _, m := range v.Months {
			exempt := "-" // no domestic disposals, nothing to exempt
			switch {
			case m.Exempt:
				exempt = "yes"
			case m.ExemptionLimit != "0.00":
				exempt = "no"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", m.Month, m.Sales, m.Gain, exempt, m.TaxableGain, m.EstimatedTax, m.ForeignSales, m.ForeignGain)
		}
		ew.check(tw.Flush())

		ew.printf("\nDisposals by asset\n")
		tw = tabwriter.NewWriter(ew, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(tw, "MONTH\tASSET\tREGIME\tQTY\tPROCEEDS\tCOST\tGAIN\t")
		for _, m := range v.Months {
			for _, a := range m.Assets {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", m.Month, a.Asset, a.Regime, a.Qty, a.Proceeds, a.Cost, a.Gain)
			}
		}
		ew.check(tw.Flush())
	}

	if f := v.Foreign; f != nil {
		ew.printf("\nForeign regime (annual): net gain %s, taxable %s, rate %s, estimated tax %s\n", f.Gain, f.TaxableGain, f.Rate, f.EstimatedTax)
	}
	ew.printf("\nEstimated tax for %d: %s BRL\n", v.Year, v.EstimatedTax)

	var flags []string
	for _, m := range v.Months {
		for _, f := range m.Flags {
			flags = append(flags, fmt.Sprintf("  %s  %s: %s. Due: %s.%s", m.Month, f.Title, f.Detail, f.Due, verifiedMark(f.Verified)))
		}
	}
	if f := v.Foreign; f != nil {
		flags = append(flags, fmt.Sprintf("  %d     %s: %s. Due: %s.%s", v.Year, f.Flag.Title, f.Flag.Detail, f.Flag.Due, verifiedMark(f.Flag.Verified)))
	}
	for _, f := range v.YearFlags {
		flags = append(flags, fmt.Sprintf("  %d     %s: %s. Due: %s.%s", v.Year, f.Title, f.Detail, f.Due, verifiedMark(f.Verified)))
	}
	if len(flags) > 0 {
		ew.printf("\nObligations to check\n%s\n", strings.Join(flags, "\n"))
	}

	if len(v.YearEnd) > 0 {
		ew.printf("\nPositions at %d-12-31 (weighted average cost, BRL)\n", v.Year)
		tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(tw, "ASSET\tQTY\tCOST\tAVG COST\tDECLARE\t")
		for _, p := range v.YearEnd {
			declare := "no"
			if p.MustDeclare != nil && *p.MustDeclare {
				declare = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", p.Asset, p.Qty, p.Cost, p.AverageCost, declare)
		}
		ew.check(tw.Flush())
	}
	return ew.err
}

// errWriter keeps the first write error so the table code stays readable.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

func (e *errWriter) printf(format string, args ...any) { fmt.Fprintf(e, format, args...) }

func (e *errWriter) check(err error) {
	if e.err == nil {
		e.err = err
	}
}
