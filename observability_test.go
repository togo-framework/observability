package observability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareRecordsRequest(t *testing.T) {
	s := newService()
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
	}))
	req := httptest.NewRequest("POST", "/orders", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	h.ServeHTTP(httptest.NewRecorder(), req)

	recs := s.Requests()
	if len(recs) != 1 {
		t.Fatalf("want 1 request, got %d", len(recs))
	}
	r := recs[0]
	if r.Method != "POST" || r.Path != "/orders" || r.Status != 201 {
		t.Errorf("bad record: %+v", r)
	}
	if r.IP != "203.0.113.7" {
		t.Errorf("ip = %q", r.IP)
	}
	if r.DurationMS < 0 {
		t.Errorf("duration = %v", r.DurationMS)
	}
}

func TestRecoverCapturesPanic(t *testing.T) {
	s := newService()
	h := s.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	if w.Code != 500 {
		t.Errorf("want 500, got %d", w.Code)
	}
	if len(s.Issues()) != 1 {
		t.Fatalf("panic should produce 1 issue, got %d", len(s.Issues()))
	}
}

func TestCaptureErrorGroupsByFingerprint(t *testing.T) {
	s := newService()
	s.CaptureError(context.Background(), errors.New("db timeout"), nil)
	s.CaptureError(context.Background(), errors.New("db timeout"), map[string]any{"q": "SELECT"})
	s.CaptureError(context.Background(), errors.New("nil pointer"), nil)

	issues := s.Issues()
	if len(issues) != 2 {
		t.Fatalf("want 2 issues (grouped), got %d", len(issues))
	}
	var dbIssue *Issue
	for i := range issues {
		if issues[i].Title == "db timeout" {
			dbIssue = &issues[i]
		}
	}
	if dbIssue == nil || dbIssue.Count != 2 {
		t.Fatalf("db timeout should have count 2, got %+v", dbIssue)
	}
	if ev := s.Events(dbIssue.Fingerprint); len(ev) != 2 {
		t.Errorf("want 2 events for fingerprint, got %d", len(ev))
	}
	// nil errors are ignored.
	s.CaptureError(context.Background(), nil, nil)
	if len(s.Issues()) != 2 {
		t.Errorf("nil error should be ignored")
	}
}

func TestSlogHandlerCapturesAndFilters(t *testing.T) {
	s := newService()
	log := slog.New(s.SlogHandler(slog.LevelInfo))
	log.Debug("ignored below level")
	log.Info("hello", "user", "alice")
	log.Error("kaboom")

	all := s.Logs("")
	if len(all) != 2 { // debug filtered out by level
		t.Fatalf("want 2 captured logs, got %d", len(all))
	}
	if got := s.Logs("ERROR"); len(got) != 1 || got[0].Message != "kaboom" {
		t.Errorf("level filter failed: %+v", got)
	}
	// attrs are captured
	for _, l := range all {
		if l.Message == "hello" && l.Attrs["user"] != "alice" {
			t.Errorf("attrs not captured: %+v", l.Attrs)
		}
	}
}

func TestHealthChecks(t *testing.T) {
	s := newService()
	s.RegisterCheck("ok", func(context.Context) error { return nil })
	if h := s.Health(context.Background()); h.Status != "ok" {
		t.Fatalf("want ok, got %+v", h)
	}
	s.RegisterCheck("bad", func(context.Context) error { return errors.New("down") })
	h := s.Health(context.Background())
	if h.Status != "degraded" {
		t.Fatalf("want degraded, got %+v", h)
	}
	if h.Checks["bad"] != "down" || h.Checks["ok"] != "ok" {
		t.Errorf("checks = %+v", h.Checks)
	}
}

func TestMetricsPercentiles(t *testing.T) {
	s := newService()
	// 100 requests, durations 1..100ms on /api; 5 of them 500s.
	for i := 1; i <= 100; i++ {
		status := 200
		if i%20 == 0 {
			status = 500
		}
		s.recordRequest(RequestRecord{Path: "/api", Status: status, DurationMS: float64(i)})
	}
	m := s.Metrics()
	if m.Requests != 100 {
		t.Fatalf("requests = %d", m.Requests)
	}
	if m.ErrorRate != 0.05 {
		t.Errorf("error rate = %v, want 0.05", m.ErrorRate)
	}
	if m.P50MS < 45 || m.P50MS > 55 {
		t.Errorf("p50 = %v, want ~50", m.P50MS)
	}
	if m.P95MS < 90 || m.P95MS > 100 {
		t.Errorf("p95 = %v, want ~95", m.P95MS)
	}
	if len(m.Slowest) != 1 || m.Slowest[0].Path != "/api" {
		t.Errorf("slowest = %+v", m.Slowest)
	}
}
