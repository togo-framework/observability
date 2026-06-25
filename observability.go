// Package observability gives a togo app built-in request inspection, error
// tracking, structured-log capture, health checks and metrics — a lightweight,
// app-internal Telescope/Pulse for Go.
//
// Everything is recorded into bounded in-memory ring buffers (with a pluggable
// seam for a durable store) and exposed over a Go API plus REST at
// /api/observability/* (+ /healthz, /readyz).
//
//	o, _ := observability.FromKernel(k)
//	router.Use(o.Middleware)              // record every request
//	router.Use(o.Recover)                 // capture panics as errors
//	slog.SetDefault(slog.New(o.SlogHandler(slog.LevelInfo)))
//	o.RegisterCheck("db", func(ctx context.Context) error { return db.PingContext(ctx) })
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/togo-framework/togo"
)

// RequestRecord is one observed HTTP request.
type RequestRecord struct {
	ID         string    `json:"id"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMS float64   `json:"duration_ms"`
	IP         string    `json:"ip"`
	Time       time.Time `json:"time"`
}

// Issue is a group of error events sharing a fingerprint.
type Issue struct {
	Fingerprint string    `json:"fingerprint"`
	Title       string    `json:"title"`
	Level       string    `json:"level"`
	Count       int       `json:"count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// ErrorEvent is a single captured error.
type ErrorEvent struct {
	Fingerprint string         `json:"fingerprint"`
	Message     string         `json:"message"`
	Context     map[string]any `json:"context,omitempty"`
	Time        time.Time      `json:"time"`
}

// LogRecord is one captured structured log line.
type LogRecord struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Time    time.Time      `json:"time"`
}

// Service is the observability runtime stored on the kernel (k.Get("observability")).
type Service struct {
	mu       sync.Mutex
	requests []RequestRecord
	issues   map[string]*Issue
	events   []ErrorEvent
	logs     []LogRecord
	checks   map[string]func(ctx context.Context) error
	max      int
}

func init() {
	togo.RegisterProviderFunc("observability", togo.PriorityLate+10, func(k *togo.Kernel) error {
		s := newService()
		k.Set("observability", s)
		if k.Router != nil {
			s.mountRoutes(k.Router)
		}
		return nil
	})
}

func newService() *Service {
	return &Service{
		issues: map[string]*Issue{},
		checks: map[string]func(ctx context.Context) error{},
		max:    1000,
	}
}

// FromKernel returns the observability Service.
func FromKernel(k *togo.Kernel) (*Service, bool) {
	v, ok := k.Get("observability")
	if !ok {
		return nil, false
	}
	s, ok := v.(*Service)
	return s, ok
}

// ── Request inspector ──────────────────────────────────────────────

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(c int) {
	r.status = c
	r.ResponseWriter.WriteHeader(c)
}

// Middleware records every request (method, path, status, duration, IP).
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.recordRequest(RequestRecord{
			ID:         newID(),
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     rec.status,
			DurationMS: float64(time.Since(start).Microseconds()) / 1000.0,
			IP:         clientIP(r),
			Time:       start,
		})
	})
}

// Recover captures panics as errors and returns a 500.
func (s *Service) Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rv := recover(); rv != nil {
				s.CaptureError(r.Context(), fmt.Errorf("panic: %v", rv), map[string]any{"path": r.URL.Path})
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Service) recordRequest(r RequestRecord) {
	s.mu.Lock()
	s.requests = appendBounded(s.requests, r, s.max)
	s.mu.Unlock()
}

// Requests returns recent observed requests (most recent last).
func (s *Service) Requests() []RequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RequestRecord, len(s.requests))
	copy(out, s.requests)
	return out
}

// ── Error tracking ─────────────────────────────────────────────────

// CaptureError records an error, grouping by fingerprint into an Issue.
func (s *Service) CaptureError(_ context.Context, err error, ctxData map[string]any) {
	if err == nil {
		return
	}
	msg := err.Error()
	fp := fingerprint(msg)
	now := time.Now()
	s.mu.Lock()
	iss := s.issues[fp]
	if iss == nil {
		iss = &Issue{Fingerprint: fp, Title: msg, Level: "error", FirstSeen: now}
		s.issues[fp] = iss
	}
	iss.Count++
	iss.LastSeen = now
	s.events = appendBounded(s.events, ErrorEvent{Fingerprint: fp, Message: msg, Context: ctxData, Time: now}, s.max)
	s.mu.Unlock()
}

// Issues returns the error groups.
func (s *Service) Issues() []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Issue, 0, len(s.issues))
	for _, i := range s.issues {
		out = append(out, *i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].LastSeen.After(out[b].LastSeen) })
	return out
}

// Events returns the error events for an issue fingerprint.
func (s *Service) Events(fingerprint string) []ErrorEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ErrorEvent
	for _, e := range s.events {
		if e.Fingerprint == fingerprint {
			out = append(out, e)
		}
	}
	return out
}

// ── Log capture ────────────────────────────────────────────────────

type logHandler struct {
	s     *Service
	level slog.Level
}

func (h *logHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	h.s.recordLog(LogRecord{Level: r.Level.String(), Message: r.Message, Attrs: attrs, Time: r.Time})
	return nil
}

func (h *logHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logHandler) WithGroup(string) slog.Handler      { return h }

// SlogHandler returns a slog.Handler that captures records into the inspector.
// Compose it with a real handler via slog routing, or set it as the default.
func (s *Service) SlogHandler(level slog.Level) slog.Handler { return &logHandler{s: s, level: level} }

func (s *Service) recordLog(r LogRecord) {
	s.mu.Lock()
	s.logs = appendBounded(s.logs, r, s.max)
	s.mu.Unlock()
}

// Logs returns captured logs, optionally filtered by level (e.g. "ERROR").
func (s *Service) Logs(level string) []LogRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if level == "" {
		out := make([]LogRecord, len(s.logs))
		copy(out, s.logs)
		return out
	}
	level = strings.ToUpper(level)
	var out []LogRecord
	for _, l := range s.logs {
		if strings.ToUpper(l.Level) == level {
			out = append(out, l)
		}
	}
	return out
}

// ── Health ─────────────────────────────────────────────────────────

// RegisterCheck adds a named readiness check.
func (s *Service) RegisterCheck(name string, fn func(ctx context.Context) error) {
	s.mu.Lock()
	s.checks[name] = fn
	s.mu.Unlock()
}

// HealthResult is the outcome of running the readiness checks.
type HealthResult struct {
	Status string            `json:"status"` // ok | degraded
	Checks map[string]string `json:"checks"`
}

// Health runs all registered checks; status is "degraded" if any fail.
func (s *Service) Health(ctx context.Context) HealthResult {
	s.mu.Lock()
	checks := make(map[string]func(ctx context.Context) error, len(s.checks))
	for n, f := range s.checks {
		checks[n] = f
	}
	s.mu.Unlock()

	res := HealthResult{Status: "ok", Checks: map[string]string{}}
	for n, f := range checks {
		if err := f(ctx); err != nil {
			res.Checks[n] = err.Error()
			res.Status = "degraded"
		} else {
			res.Checks[n] = "ok"
		}
	}
	return res
}

// ── Metrics ────────────────────────────────────────────────────────

// EndpointStat is per-path aggregate latency.
type EndpointStat struct {
	Path  string  `json:"path"`
	AvgMS float64 `json:"avg_ms"`
	Count int     `json:"count"`
}

// Metrics is a rolling summary derived from observed requests.
type Metrics struct {
	Requests  int            `json:"requests"`
	ErrorRate float64        `json:"error_rate"`
	P50MS     float64        `json:"p50_ms"`
	P95MS     float64        `json:"p95_ms"`
	Slowest   []EndpointStat `json:"slowest"`
}

// Metrics computes request count, error rate, p50/p95 latency and slowest paths.
func (s *Service) Metrics() Metrics {
	s.mu.Lock()
	reqs := make([]RequestRecord, len(s.requests))
	copy(reqs, s.requests)
	s.mu.Unlock()

	m := Metrics{Requests: len(reqs)}
	if len(reqs) == 0 {
		return m
	}
	durs := make([]float64, len(reqs))
	var errs int
	byPath := map[string]*EndpointStat{}
	for i, r := range reqs {
		durs[i] = r.DurationMS
		if r.Status >= 500 {
			errs++
		}
		st := byPath[r.Path]
		if st == nil {
			st = &EndpointStat{Path: r.Path}
			byPath[r.Path] = st
		}
		st.AvgMS = (st.AvgMS*float64(st.Count) + r.DurationMS) / float64(st.Count+1)
		st.Count++
	}
	m.ErrorRate = float64(errs) / float64(len(reqs))
	sort.Float64s(durs)
	m.P50MS = percentile(durs, 50)
	m.P95MS = percentile(durs, 95)

	stats := make([]EndpointStat, 0, len(byPath))
	for _, st := range byPath {
		stats = append(stats, *st)
	}
	sort.Slice(stats, func(a, b int) bool { return stats[a].AvgMS > stats[b].AvgMS })
	if len(stats) > 5 {
		stats = stats[:5]
	}
	m.Slowest = stats
	return m
}

func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ── REST ───────────────────────────────────────────────────────────

func (s *Service) mountRoutes(r chi.Router) {
	r.Route("/api/observability", func(r chi.Router) {
		r.Get("/requests", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.Requests()) })
		r.Get("/issues", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.Issues()) })
		r.Get("/issues/{fp}/events", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, s.Events(chi.URLParam(req, "fp")))
		})
		r.Get("/logs", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, s.Logs(req.URL.Query().Get("level")))
		})
		r.Get("/metrics", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.Metrics()) })
		r.Get("/health", func(w http.ResponseWriter, req *http.Request) { writeJSON(w, 200, s.Health(req.Context())) })
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		h := s.Health(req.Context())
		code := http.StatusOK
		if h.Status != "ok" {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, h)
	})
}

// ── helpers ────────────────────────────────────────────────────────

var idSeq atomic.Uint64

func newID() string { return strconv.FormatUint(idSeq.Add(1), 36) }

func fingerprint(msg string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(msg))
	return strconv.FormatUint(h.Sum64(), 16)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func appendBounded[T any](buf []T, v T, max int) []T {
	buf = append(buf, v)
	if len(buf) > max {
		buf = buf[len(buf)-max:]
	}
	return buf
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
