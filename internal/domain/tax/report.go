package tax

import (
	"fmt"
	"sort"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/domain"
)

// Regime says which set of rules taxes a disposal.
type Regime string

// Regimes.
const (
	RegimeDomestic Regime = "domestic" // monthly capital gains
	RegimeForeign  Regime = "foreign"  // annual, assets held abroad
)

// Operation is any valued trade, used for reporting thresholds that count
// purchases as well as sales.
type Operation struct {
	Time     time.Time
	Exchange string
	Value    domain.Decimal // BRL
}

// Input is everything Evaluate needs for one calendar year.
type Input struct {
	Year       int
	Location   *time.Location // month boundaries follow the taxpayer's local time
	Disposals  []domain.Disposal
	Operations []Operation
	// YearEnd holds the positions at the end of the year (local time).
	YearEnd []domain.Position
}

// Flag is a reporting or payment obligation the user should check.
type Flag struct {
	RuleID   string
	Title    string
	Detail   string
	Due      string
	Source   string
	Verified bool
}

// AssetLine aggregates one asset's disposals in a month under one regime.
type AssetLine struct {
	Asset     string
	Regime    Regime
	Disposals int
	Qty       domain.Decimal
	Proceeds  domain.Decimal
	Cost      domain.Decimal
	Gain      domain.Decimal
}

// MonthReport is the result for one month.
type MonthReport struct {
	Month time.Month

	// Domestic monthly regime.
	Sales          domain.Decimal // total proceeds of domestic disposals
	Gain           domain.Decimal // net gain of domestic disposals
	ExemptionLimit domain.Decimal // zero when no exemption rule applies
	Exempt         bool
	TaxableGain    domain.Decimal
	EstimatedTax   domain.Decimal

	// Foreign disposals are listed per month but taxed annually.
	ForeignSales domain.Decimal
	ForeignGain  domain.Decimal

	// Operations is the BRL value of trades counted for reporting rules.
	Operations        domain.Decimal
	ForeignOperations domain.Decimal

	Assets []AssetLine
	Flags  []Flag
}

// ForeignSummary is the annual regime for assets held abroad.
type ForeignSummary struct {
	Gain         domain.Decimal // net over the year
	TaxableGain  domain.Decimal
	Rate         domain.Decimal
	EstimatedTax domain.Decimal
	Flag         Flag
}

// PositionLine is a year-end holding.
type PositionLine struct {
	Asset       string
	Qty         domain.Decimal
	Cost        domain.Decimal
	AverageCost domain.Decimal
	MustDeclare bool
}

// YearReport is the full result for one calendar year.
type YearReport struct {
	Year            int
	RulesetID       string
	RulesetVerified bool
	Disclaimer      string
	Months          []MonthReport // only months with activity
	Foreign         *ForeignSummary
	YearEnd         []PositionLine
	YearFlags       []Flag
	EstimatedTax    domain.Decimal // domestic months + foreign annual
}

// Evaluate applies rs to one year of activity. It never guesses: a month for
// which the ruleset has no capital-gains rate is an error.
func Evaluate(rs Ruleset, in Input) (YearReport, error) {
	loc := in.Location
	if loc == nil {
		loc = time.UTC
	}
	rep := YearReport{Year: in.Year, RulesetID: rs.ID, RulesetVerified: rs.Verified, Disclaimer: rs.Disclaimer}

	months := map[time.Month]*monthAcc{}
	acc := func(m time.Month) *monthAcc {
		a, ok := months[m]
		if !ok {
			a = &monthAcc{assets: map[assetKey]*AssetLine{}}
			months[m] = a
		}
		return a
	}

	var foreignDisposals []domain.Disposal
	for _, d := range in.Disposals {
		lt := d.Time.In(loc)
		if lt.Year() != in.Year {
			continue
		}
		regime := RegimeDomestic
		if _, ok := activeAt(rs.ForeignRegimes, lt); ok && rs.IsForeign(d.Exchange, lt) {
			regime = RegimeForeign
			foreignDisposals = append(foreignDisposals, d)
		}
		acc(lt.Month()).add(d, regime)
	}
	for _, op := range in.Operations {
		lt := op.Time.In(loc)
		if lt.Year() != in.Year {
			continue
		}
		a := acc(lt.Month())
		a.operations = a.operations.Add(op.Value)
		if rs.IsForeign(op.Exchange, lt) {
			a.foreignOps = a.foreignOps.Add(op.Value)
		}
	}

	keys := make([]time.Month, 0, len(months))
	for m := range months {
		keys = append(keys, m)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, m := range keys {
		mr, err := evaluateMonth(rs, time.Date(in.Year, m, 1, 0, 0, 0, 0, time.UTC), months[m])
		if err != nil {
			return YearReport{}, err
		}
		rep.EstimatedTax = rep.EstimatedTax.Add(mr.EstimatedTax)
		rep.Months = append(rep.Months, mr)
	}

	if len(foreignDisposals) > 0 {
		lastDay := time.Date(in.Year, time.December, 31, 0, 0, 0, 0, time.UTC)
		fr, _ := activeAt(rs.ForeignRegimes, lastDay)
		fs := foreignSummary(fr, foreignDisposals)
		rep.EstimatedTax = rep.EstimatedTax.Add(fs.EstimatedTax)
		rep.Foreign = &fs
	}

	rep.YearEnd, rep.YearFlags = yearEnd(rs, in)
	return rep, nil
}

type assetKey struct {
	asset  string
	regime Regime
}

type monthAcc struct {
	domestic   []domain.Disposal
	foreign    []domain.Disposal
	assets     map[assetKey]*AssetLine
	operations domain.Decimal
	foreignOps domain.Decimal
}

func (a *monthAcc) add(d domain.Disposal, regime Regime) {
	if regime == RegimeForeign {
		a.foreign = append(a.foreign, d)
	} else {
		a.domestic = append(a.domestic, d)
	}
	k := assetKey{d.Asset, regime}
	line, ok := a.assets[k]
	if !ok {
		line = &AssetLine{Asset: d.Asset, Regime: regime}
		a.assets[k] = line
	}
	line.Disposals++
	line.Qty = line.Qty.Add(d.Qty)
	line.Proceeds = line.Proceeds.Add(d.Proceeds)
	line.Cost = line.Cost.Add(d.Cost)
	line.Gain = line.Gain.Add(d.Gain())
}

func evaluateMonth(rs Ruleset, first time.Time, a *monthAcc) (MonthReport, error) {
	mr := MonthReport{Month: first.Month(), Operations: a.operations, ForeignOperations: a.foreignOps}
	for _, d := range a.domestic {
		mr.Sales = mr.Sales.Add(d.Proceeds)
		mr.Gain = mr.Gain.Add(d.Gain())
	}
	for _, d := range a.foreign {
		mr.ForeignSales = mr.ForeignSales.Add(d.Proceeds)
		mr.ForeignGain = mr.ForeignGain.Add(d.Gain())
	}
	for _, l := range a.assets {
		mr.Assets = append(mr.Assets, *l)
	}
	sort.Slice(mr.Assets, func(i, j int) bool {
		if mr.Assets[i].Regime != mr.Assets[j].Regime {
			return mr.Assets[i].Regime < mr.Assets[j].Regime
		}
		return mr.Assets[i].Asset < mr.Assets[j].Asset
	})

	if len(a.domestic) > 0 {
		rate, ok := activeAt(rs.CapitalGainRates, first)
		if !ok {
			return MonthReport{}, fmt.Errorf("%w: ruleset %q has no capital_gain_rates for %s", domain.ErrNoRulesetPeriod, rs.ID, first.Format("2006-01"))
		}
		if ex, ok := activeAt(rs.Exemptions, first); ok {
			mr.ExemptionLimit = ex.SalesLimit
			mr.Exempt = mr.Sales.Cmp(ex.SalesLimit) <= 0
		}
		if !mr.Exempt {
			mr.TaxableGain = taxableGain(a.domestic, rate.OffsetLossesInMonth)
			mr.EstimatedTax = ProgressiveTax(mr.TaxableGain, rate.Brackets)
		}
		if mr.EstimatedTax.Sign() > 0 {
			mr.Flags = append(mr.Flags, Flag{
				RuleID: "capital_gain_payment", Title: "Capital-gains tax payment",
				Detail: fmt.Sprintf("estimated tax on domestic disposals; payment code %s", rate.PaymentCode),
				Due:    rate.PaymentDue,
				Source: rate.Source, Verified: rate.Verified,
			})
		}
	}

	for _, r := range rs.Reporting {
		if r.Kind != MonthlyOperations || !r.Covers(first) {
			continue
		}
		volume := mr.Operations
		if r.Scope == ScopeForeign {
			volume = mr.ForeignOperations
		}
		if volume.Cmp(r.Threshold) > 0 {
			mr.Flags = append(mr.Flags, Flag{
				RuleID: r.ID, Title: r.Title,
				Detail: fmt.Sprintf("operations in scope %q total %s BRL, above %s BRL", r.Scope, volume.StringFixed(2), r.Threshold.StringFixed(2)),
				Due:    r.Due, Source: r.Source, Verified: r.Verified,
			})
		}
	}
	return mr, nil
}

// taxableGain sums gains. Without loss offsetting, each disposal stands on
// its own and a loss is simply not taxed.
func taxableGain(ds []domain.Disposal, offset bool) domain.Decimal {
	sum := domain.Zero
	for _, d := range ds {
		g := d.Gain()
		if !offset {
			g = g.Max(domain.Zero)
		}
		sum = sum.Add(g)
	}
	return sum.Max(domain.Zero)
}

func foreignSummary(fr ForeignRegime, ds []domain.Disposal) ForeignSummary {
	fs := ForeignSummary{Rate: fr.Rate}
	for _, d := range ds {
		fs.Gain = fs.Gain.Add(d.Gain())
	}
	fs.TaxableGain = taxableGain(ds, fr.OffsetLossesInYear)
	fs.EstimatedTax = fs.TaxableGain.Mul(fr.Rate)
	fs.Flag = Flag{
		RuleID: "foreign_annual", Title: "Annual tax on assets held abroad",
		Detail: fmt.Sprintf("disposals on foreign exchanges are taxed annually at %s%%", fr.Rate.Mul(domain.NewDecimalFromInt(100)).String()),
		Due:    fr.PaymentDue, Source: fr.Source, Verified: fr.Verified,
	}
	return fs
}

func yearEnd(rs Ruleset, in Input) ([]PositionLine, []Flag) {
	lastDay := time.Date(in.Year, time.December, 31, 0, 0, 0, 0, time.UTC)
	var rules []ReportingRule
	for _, r := range rs.Reporting {
		if r.Kind == YearEndPosition && r.Covers(lastDay) {
			rules = append(rules, r)
		}
	}
	var lines []PositionLine
	var flags []Flag
	for _, p := range in.YearEnd {
		line := PositionLine{Asset: p.Asset, Qty: p.Qty, Cost: p.Cost, AverageCost: p.AverageCost()}
		for _, r := range rules {
			if p.Cost.Cmp(r.Threshold) >= 0 {
				line.MustDeclare = true
			}
		}
		lines = append(lines, line)
	}
	for _, r := range rules {
		var assets []string
		for _, l := range lines {
			if l.Cost.Cmp(r.Threshold) >= 0 {
				assets = append(assets, l.Asset)
			}
		}
		if len(assets) > 0 {
			flags = append(flags, Flag{
				RuleID: r.ID, Title: r.Title,
				Detail: fmt.Sprintf("year-end cost at or above %s BRL: %v", r.Threshold.StringFixed(2), assets),
				Due:    r.Due, Source: r.Source, Verified: r.Verified,
			})
		}
	}
	return lines, flags
}
