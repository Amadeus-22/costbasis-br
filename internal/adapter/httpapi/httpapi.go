// Package httpapi exposes the same use cases as the CLI over a local HTTP API.
// It binds to 127.0.0.1 by default: the data is private and there is no
// authentication in the MVP.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Amadeus-22/costbasis-br/internal/adapter/render"
	"github.com/Amadeus-22/costbasis-br/internal/app"
	"github.com/Amadeus-22/costbasis-br/internal/domain"
	"github.com/Amadeus-22/costbasis-br/internal/domain/tax"
)

// MaxImportBytes caps an uploaded export. Binance allows 10,000 rows per
// export, far below this.
const MaxImportBytes = 32 << 20

// Service is the subset of app.Service the API uses; tests supply a fake.
type Service interface {
	Import(ctx context.Context, format string, r io.Reader) (app.ImportResult, error)
	Report(ctx context.Context, year int, rulesetID string) (tax.YearReport, error)
	Positions(ctx context.Context, date time.Time) ([]domain.Position, error)
	Ready(ctx context.Context) error
}

// API is the HTTP handler set.
type API struct {
	svc            Service
	defaultRuleset string
	loc            *time.Location
	log            *slog.Logger
	metrics        *metrics
	registry       *prometheus.Registry
}

// New builds the API with its own Prometheus registry.
func New(svc Service, defaultRuleset string, loc *time.Location, log *slog.Logger) *API {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return &API{svc: svc, defaultRuleset: defaultRuleset, loc: loc, log: log, metrics: newMetrics(reg), registry: reg}
}

// Handler returns the routed, instrumented handler.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /v1/imports", a.postImport)
	mux.HandleFunc("GET /v1/reports/{year}", a.getReport)
	mux.HandleFunc("GET /v1/positions", a.getPositions)
	return a.instrument(mux)
}

func (a *API) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.svc.Ready(ctx); err != nil {
		a.log.WarnContext(ctx, "readiness check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database is not reachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// POST /v1/imports?format=binance with the export file as the body.
func (a *API) postImport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "generic"
	}
	body := http.MaxBytesReader(w, r.Body, MaxImportBytes)
	res, err := a.svc.Import(r.Context(), format, body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("import body exceeds %d bytes", MaxImportBytes))
			return
		}
		a.fail(w, r, err)
		return
	}
	a.metrics.imported.WithLabelValues(res.Format, "inserted").Add(float64(res.Inserted))
	a.metrics.imported.WithLabelValues(res.Format, "duplicate").Add(float64(res.Duplicates))
	writeJSON(w, http.StatusOK, res)
}

// GET /v1/reports/{year}?ruleset=br-example
func (a *API) getReport(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "year must be a number, e.g. /v1/reports/2026")
		return
	}
	ruleset := r.URL.Query().Get("ruleset")
	if ruleset == "" {
		ruleset = a.defaultRuleset
	}
	rep, err := a.svc.Report(r.Context(), year, ruleset)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, render.NewReportView(rep))
}

// GET /v1/positions?date=2026-12-31 (default: today in the configured zone)
func (a *API) getPositions(w http.ResponseWriter, r *http.Request) {
	date := time.Now().In(a.loc)
	if s := r.URL.Query().Get("date"); s != "" {
		d, err := time.ParseInLocation(time.DateOnly, s, a.loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "date must be YYYY-MM-DD")
			return
		}
		date = d
	}
	ps, err := a.svc.Positions(r.Context(), date)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": date.Format(time.DateOnly), "positions": render.NewPositionViews(ps)})
}

// fail maps domain and app errors to status codes. User-data problems get
// their message back verbatim (it names the row, date or pair to fix);
// anything unexpected is logged and reported generically.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrUnknownFormat), errors.Is(err, app.ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
	case errors.Is(err, tax.ErrRulesetNotFound):
		writeError(w, http.StatusNotFound, "ruleset_not_found", err.Error())
	case errors.Is(err, app.ErrInvalidImport), errors.Is(err, domain.ErrInvalidTransaction):
		writeError(w, http.StatusUnprocessableEntity, "invalid_import", err.Error())
	case errors.Is(err, domain.ErrInsufficientBalance):
		writeError(w, http.StatusUnprocessableEntity, "incomplete_history", err.Error())
	case errors.Is(err, domain.ErrRateNotFound):
		writeError(w, http.StatusUnprocessableEntity, "rate_not_found", err.Error())
	case errors.Is(err, domain.ErrNoRulesetPeriod):
		writeError(w, http.StatusUnprocessableEntity, "no_rule_for_period", err.Error())
	case errors.Is(err, context.Canceled):
		// Client went away; nothing useful to write.
	default:
		a.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error; see server logs")
	}
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, msg
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = render.WriteJSON(w, v) // the status line is sent; a write error means the client left
}

// --- metrics ---

type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	imported *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "costbasis_http_requests_total", Help: "HTTP requests by route, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "costbasis_http_request_duration_seconds", Help: "HTTP request latency by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		imported: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "costbasis_imported_transactions_total", Help: "Transactions received by import, by format and result (inserted or duplicate).",
		}, []string{"format", "result"}),
	}
	reg.MustRegister(m.requests, m.duration, m.imported)
	return m
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// instrument records metrics per route pattern (never the raw path, which
// would explode label cardinality) and logs each request.
func (a *API) instrument(next *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)
		a.metrics.requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		a.metrics.duration.WithLabelValues(route).Observe(elapsed.Seconds())
		a.log.DebugContext(r.Context(), "http request", "method", r.Method, "route", route, "status", rec.code, "duration_ms", elapsed.Milliseconds())
	})
}

// --- server lifecycle ---

// Serve runs an HTTP server on addr until ctx is cancelled, then shuts down
// gracefully, giving in-flight requests up to 10 seconds.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second, // large imports
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Info("http server shutting down")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
