# observability — usage

```go
o, _ := observability.FromKernel(k)

// Request inspector + panic capture (wrap your router).
router.Use(o.Middleware)
router.Use(o.Recover)

// Route structured logs into the inspector.
slog.SetDefault(slog.New(o.SlogHandler(slog.LevelInfo)))

// Capture handled errors explicitly.
if err := doWork(ctx); err != nil {
    o.CaptureError(ctx, err, map[string]any{"order_id": id})
}

// Readiness checks.
o.RegisterCheck("db", func(ctx context.Context) error { return db.PingContext(ctx) })
o.RegisterCheck("cache", func(ctx context.Context) error { return cache.Ping(ctx) })
```

## What it records
- **Requests** — method, path, status, duration, IP (ring buffer). `o.Requests()`
- **Errors** — grouped into Issues by fingerprint with counts + first/last seen. `o.Issues()`, `o.Events(fp)`
- **Logs** — captured slog records, filterable by level. `o.Logs("ERROR")`
- **Health** — `o.Health(ctx)` runs the registered checks → `ok` / `degraded`
- **Metrics** — `o.Metrics()` → request count, error rate, p50/p95 latency, slowest endpoints

## REST
| Method | Path | Returns |
|---|---|---|
| GET | `/api/observability/requests` | recent requests |
| GET | `/api/observability/issues` | error groups |
| GET | `/api/observability/issues/{fp}/events` | events for an issue |
| GET | `/api/observability/logs?level=ERROR` | captured logs |
| GET | `/api/observability/metrics` | metrics summary |
| GET | `/api/observability/health` | health detail |
| GET | `/healthz` | liveness (always ok) |
| GET | `/readyz` | readiness (503 if degraded) |

Records live in bounded in-memory ring buffers (newest ~1000); the stores have a
seam for a durable backend.
