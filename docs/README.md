# observability — docs

App-internal request inspection, error tracking, log capture, health checks and
metrics for a togo app — a lightweight Telescope/Pulse for Go.

- **Install:** `togo install togo-framework/observability`
- **Marketplace:** https://to-go.dev/plugins/observability
- **Source:** https://github.com/togo-framework/observability

## Contents
- [Usage](usage.md) — middleware, error capture, slog handler, health checks, metrics + REST

## Overview
Resolve the service via `observability.FromKernel(k)`, add the `Middleware`
(request inspector) and `Recover` (panic→error) middleware, route slog through
`SlogHandler`, and register readiness checks with `RegisterCheck`. Everything is
queryable over a Go API and REST at `/api/observability/*` (+ `/healthz`,
`/readyz`). See [usage.md](usage.md).
