package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Amadeus-22/costbasis-br/internal/app"
	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

type fakeService struct {
	importErr, reportErr, readyErr error
	gotFormat, gotRuleset, gotBody string
	gotYear                        int
	gotDate                        time.Time
}

func (f *fakeService) Import(_ context.Context, format string, r io.Reader) (app.ImportResult, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return app.ImportResult{}, err
	}
	f.gotFormat, f.gotBody = format, string(b)
	if f.importErr != nil {
		return app.ImportResult{}, f.importErr
	}
	return app.ImportResult{Format: format, Parsed: 3, Inserted: 2, Duplicates: 1}, nil
}

func (f *fakeService) Report(_ context.Context, year int, ruleset string) (tax.YearReport, error) {
	f.gotYear, f.gotRuleset = year, ruleset
	if f.reportErr != nil {
		return tax.YearReport{}, f.reportErr
	}
	return tax.YearReport{Year: year, RulesetID: ruleset, EstimatedTax: domain.MustDecimal("10.005")}, nil
}

func (f *fakeService) Positions(_ context.Context, date time.Time) ([]domain.Position, error) {
	f.gotDate = date
	return []domain.Position{{Asset: "BTC", Qty: domain.MustDecimal("0.5"), Cost: domain.MustDecimal("100")}}, nil
}

func (f *fakeService) Ready(context.Context) error { return f.readyErr }

func newServer(t *testing.T, svc *fakeService) *httptest.Server {
	t.Helper()
	api := New(svc, "br-example", time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func TestHealthAndReadiness(t *testing.T) {
	svc := &fakeService{}
	srv := newServer(t, svc)
	if code, _ := do(t, "GET", srv.URL+"/healthz", ""); code != 200 {
		t.Errorf("healthz = %d", code)
	}
	if code, _ := do(t, "GET", srv.URL+"/readyz", ""); code != 200 {
		t.Errorf("readyz = %d", code)
	}
	svc.readyErr = errors.New("disk gone")
	code, body := do(t, "GET", srv.URL+"/readyz", "")
	if code != 503 || body["error"].(map[string]any)["code"] != "not_ready" {
		t.Errorf("readyz when down = %d %v", code, body)
	}
}

func TestImport(t *testing.T) {
	svc := &fakeService{}
	srv := newServer(t, svc)
	code, body := do(t, "POST", srv.URL+"/v1/imports?format=binance", "csv-bytes")
	if code != 200 || body["inserted"] != float64(2) || svc.gotFormat != "binance" || svc.gotBody != "csv-bytes" {
		t.Fatalf("import = %d %v (format %q body %q)", code, body, svc.gotFormat, svc.gotBody)
	}
	if code, _ := do(t, "POST", srv.URL+"/v1/imports", ""); code != 200 || svc.gotFormat != "generic" {
		t.Errorf("default format = %q", svc.gotFormat)
	}
	if code, _ := do(t, "GET", srv.URL+"/v1/imports", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/imports = %d", code)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		err  error
		code int
		kind string
	}{
		{fmt.Errorf("x: %w", app.ErrUnknownFormat), 400, "invalid_argument"},
		{fmt.Errorf("x: %w", app.ErrInvalidImport), 422, "invalid_import"},
		{&domain.OversellError{TxLabel: "transaction binance/T-1", Asset: "BTC"}, 422, "incomplete_history"},
		{fmt.Errorf("value: %w: 2026-01-05 USDT/BRL", domain.ErrRateNotFound), 422, "rate_not_found"},
		{fmt.Errorf("x: %w", tax.ErrRulesetNotFound), 404, "ruleset_not_found"},
		{fmt.Errorf("x: %w", domain.ErrNoRulesetPeriod), 422, "no_rule_for_period"},
		{errors.New("database exploded"), 500, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			srv := newServer(t, &fakeService{reportErr: tt.err})
			code, body := do(t, "GET", srv.URL+"/v1/reports/2026", "")
			e := body["error"].(map[string]any)
			if code != tt.code || e["code"] != tt.kind {
				t.Fatalf("got %d %v, want %d %s", code, e, tt.code, tt.kind)
			}
			if tt.code == 500 && strings.Contains(e["message"].(string), "exploded") {
				t.Error("internal error details leaked to the client")
			}
			if tt.kind == "incomplete_history" && !strings.Contains(e["message"].(string), "binance/T-1") {
				t.Error("oversell message should name the transaction")
			}
		})
	}
}

func TestReportAndPositions(t *testing.T) {
	svc := &fakeService{}
	srv := newServer(t, svc)
	code, body := do(t, "GET", srv.URL+"/v1/reports/2026?ruleset=custom", "")
	if code != 200 || svc.gotYear != 2026 || svc.gotRuleset != "custom" || body["estimated_tax"] != "10.01" {
		t.Fatalf("report = %d %v", code, body)
	}
	if do(t, "GET", srv.URL+"/v1/reports/2026", ""); svc.gotRuleset != "br-example" {
		t.Errorf("default ruleset = %q", svc.gotRuleset)
	}
	if code, _ := do(t, "GET", srv.URL+"/v1/reports/abc", ""); code != 400 {
		t.Errorf("bad year = %d", code)
	}
	code, body = do(t, "GET", srv.URL+"/v1/positions?date=2026-12-31", "")
	if code != 200 || body["date"] != "2026-12-31" || svc.gotDate.Format(time.DateOnly) != "2026-12-31" {
		t.Fatalf("positions = %d %v", code, body)
	}
	if code, _ := do(t, "GET", srv.URL+"/v1/positions?date=31/12/2026", ""); code != 400 {
		t.Errorf("bad date = %d", code)
	}
}

func TestMetricsUseRoutePatterns(t *testing.T) {
	srv := newServer(t, &fakeService{})
	do(t, "GET", srv.URL+"/v1/reports/2026", "")
	do(t, "POST", srv.URL+"/v1/imports", "x")
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	text := string(b)
	for _, want := range []string{
		`costbasis_http_requests_total{code="200",method="GET",route="GET /v1/reports/{year}"} 1`,
		`costbasis_imported_transactions_total{format="generic",result="inserted"} 2`,
		`costbasis_http_request_duration_seconds_bucket`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}

func TestServeShutsDownOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	h := New(&fakeService{}, "br-example", time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	go func() { done <- Serve(ctx, addr, h, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}
