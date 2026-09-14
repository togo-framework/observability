<div align="center">
  <picture><source media="(prefers-color-scheme: dark)" srcset=".github/assets/togo-mark-dark.svg" /><img src=".github/assets/togo-mark.svg" alt="ToGO" height="64" /></picture>
  <h1>togo-framework/observability</h1>
  <p>
    <a href="https://to-go.dev/marketplace"><img src="https://img.shields.io/badge/marketplace-to--go.dev-1F8A99" alt="marketplace" /></a>
    <a href="https://pkg.go.dev/github.com/togo-framework/observability"><img src="https://pkg.go.dev/badge/github.com/togo-framework/observability.svg" alt="pkg.go.dev" /></a>
    <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT" />
  </p>
  <p><strong>App-internal observability for <a href="https://to-go.dev">togo</a> — request inspector, error tracking, log capture, health checks & metrics.</strong></p>
</div>

## Install

```bash
togo install togo-framework/observability
```

A lightweight, **app-internal Telescope / Pulse** for togo: see every request, group your errors, capture structured logs, expose health endpoints, and read latency/error metrics — all from inside your app, no external service required.

## What you get

- **Request inspector** — middleware records method, path, status, duration and IP for every request.
- **Error tracking** — `CaptureError` groups exceptions by fingerprint into Issues (count, first/last seen); a panic-recovery middleware turns crashes into captured errors + a clean 500.
- **Log capture** — a `slog.Handler` records structured logs, queryable by level.
- **Health** — register readiness checks; serve `/healthz` (liveness) and `/readyz` (readiness → 503 when degraded).
- **Metrics** — request count, error rate, p50/p95 latency, and slowest endpoints.

Everything lives in bounded in-memory ring buffers (with a seam for a durable store) and is exposed over a Go API + REST at `/api/observability/*`.

## Usage

```go
o, _ := observability.FromKernel(k)

// Inspect requests + capture panics.
router.Use(o.Middleware)
router.Use(o.Recover)

// Capture structured logs.
slog.SetDefault(slog.New(o.SlogHandler(slog.LevelInfo)))

// Capture handled errors with context.
if err := charge(ctx, order); err != nil {
    o.CaptureError(ctx, err, map[string]any{"order_id": order.ID})
}

// Readiness checks for your dependencies.
o.RegisterCheck("db",    func(ctx context.Context) error { return db.PingContext(ctx) })
o.RegisterCheck("cache", func(ctx context.Context) error { return cache.Ping(ctx) })
```

Read it back:

```go
o.Requests()        // recent requests
o.Issues()          // error groups; o.Events(fingerprint) for the events
o.Logs("ERROR")     // captured logs, filtered by level
o.Health(ctx)       // ok | degraded + per-check status
o.Metrics()         // requests, error_rate, p50/p95, slowest endpoints
```

## REST

| Method | Path | Returns |
|---|---|---|
| GET | `/api/observability/requests` | recent requests |
| GET | `/api/observability/issues` | error groups |
| GET | `/api/observability/issues/{fp}/events` | events for an issue |
| GET | `/api/observability/logs?level=ERROR` | captured logs |
| GET | `/api/observability/metrics` | metrics summary |
| GET | `/api/observability/health` | health detail |
| GET | `/healthz` · `/readyz` | liveness · readiness (503 if degraded) |

## Configuration

No required env. Stores default to ~1000-entry ring buffers; replace the store seam with a durable backend for long retention or multi-instance aggregation.

---

<div align="center">
  <h3>Premium sponsors</h3>
  <p>
    <a href="https://id8media.com"><strong>ID8 Media</strong></a> &nbsp;·&nbsp;
    <a href="https://one-studio.co"><strong>One Studio</strong></a>
  </p>
  <p><sub>Support togo — <a href="https://github.com/sponsors/fadymondy">become a sponsor</a>.</sub></p>
</div>
