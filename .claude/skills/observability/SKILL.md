---
name: observability
description: Instrument a togo app with the observability plugin — request inspector + panic capture middleware, error tracking, slog capture, health checks (/healthz, /readyz) and metrics (p50/p95, error rate).
---

# togo observability

Use this skill to wire the `togo-framework/observability` plugin into a togo app.

## Wire it up
```go
o, _ := observability.FromKernel(k)
router.Use(o.Middleware)   // record every request
router.Use(o.Recover)      // capture panics as errors → 500
slog.SetDefault(slog.New(o.SlogHandler(slog.LevelInfo)))  // capture logs
o.RegisterCheck("db", func(ctx context.Context) error { return db.PingContext(ctx) })
```

## Capture handled errors
```go
o.CaptureError(ctx, err, map[string]any{"order_id": id})  // grouped by fingerprint
```

## Inspect
- `o.Requests()` · `o.Issues()` / `o.Events(fp)` · `o.Logs("ERROR")` · `o.Health(ctx)` · `o.Metrics()`
- REST: `/api/observability/{requests,issues,logs,metrics,health}`, `/healthz`, `/readyz`

## Notes
- Stores are bounded in-memory ring buffers (Telescope/Pulse-style for dev + light prod); wire a durable store for long retention.
- Register readiness checks for every external dependency; `/readyz` returns 503 when any check fails.
