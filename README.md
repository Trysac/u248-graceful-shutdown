# graceful-shutdown

Ordered graceful shutdown for Go services.

## Problem

When Kubernetes or systemd stops a Go service, in-flight requests can be cut, consumers can lose messages, and database pools can close before the HTTP server has drained. Correct shutdown needs a fixed order: stop advertising readiness, wait for load balancers to notice, drain servers, then close consumers and pools, each with a deadline; a second signal must still force cancellation. This package implements that order with deterministic tests.

## Installation

```sh
go get github.com/Trysac/u248-graceful-shutdown
```

The import path is `github.com/Trysac/u248-graceful-shutdown`; the package name is `gracefulshutdown`. The package uses the Go standard library only.

## Features

- Readiness flip with optional pre-stop delay.
- Phased shutdown hooks with per-hook timeouts.
- Panic recovery converted to typed errors.
- Second-signal force cancellation.
- HTTP server draining via `HTTPServerHook`.
- Typed errors with machine-readable codes.
- Synchronous `OnEvent` lifecycle events.
- `Done()` channel closed when shutdown finishes.

## Usage

```go
package main
import ("context"; "net/http"; "os"; "os/signal"; "syscall"; "time"; gracefulshutdown "github.com/Trysac/u248-graceful-shutdown")
func main() {
	srv := &http.Server{Addr: ":8080"}
	m := gracefulshutdown.New(gracefulshutdown.Options{PreStopDelay: 5 * time.Second})
	m.Register("http", 1, 10*time.Second, gracefulshutdown.HTTPServerHook(srv))
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	_ = gracefulshutdown.WaitForSignal(context.Background(), sigs, m, 30*time.Second)
}
```

## Public API

- `type Options struct { PreStopDelay time.Duration; Sleep func(ctx context.Context, d time.Duration) error; OnEvent func(Event) }`. `PreStopDelay` defaults to `0`; `Sleep` defaults to a timer select on `ctx`; `OnEvent` is optional.
- `func New(opts Options) *Manager` creates a manager.
- `func (m *Manager) Register(name string, phase int, timeout time.Duration, fn func(ctx context.Context) error) error` registers a hook. It returns `*Error` with code `invalid_hook` for an empty name, nil function, or timeout `<= 0`; `duplicate_hook` for a repeated name; `shutting_down` after shutdown has started. Negative phases are allowed and run before phase `0`.
- `func (m *Manager) Shutdown(ctx context.Context) error` performs shutdown. The first call flips `Ready()` to false, emits the `draining` event, calls `Sleep(ctx, PreStopDelay)` when the delay is greater than zero, runs phases in ascending phase number, and runs hooks within a phase concurrently. The result is `errors.Join` of hook errors in registration order (`nil` when none). A later call blocks until the first finishes and returns the stored error.
- `func (m *Manager) Ready() bool` reports whether the service is still advertising readiness.
- `func (m *Manager) Done() <-chan struct{}` is closed when shutdown finishes.
- `func WaitForSignal(ctx context.Context, sigs <-chan os.Signal, m *Manager, deadline time.Duration) error` waits for one signal or `ctx.Done()`. On a signal it starts `Shutdown` with `context.WithTimeout(context.WithoutCancel(ctx), deadline)`; a second signal cancels that context. It returns `Shutdown`'s error. If `ctx` is cancelled before any signal, it returns `ctx.Err()` without calling `Shutdown`.
- `func HTTPServerHook(srv *http.Server) func(ctx context.Context) error` calls `srv.Shutdown(ctx)`.
- `type Event struct { Type string; Hook string; Phase int }` has types `draining`, `phase_started`, `hook_done`, `hook_failed`, and `done`. `OnEvent` runs synchronously and never receives hook error text.
- `type Error struct { Code string; Hook string; Err error }` implements `Unwrap`. Codes are `invalid_hook`, `duplicate_hook`, `shutting_down`, `hook_timeout`, `hook_panic`, and `skipped`.

## Limits

- A hook that ignores its context is abandoned when its per-hook timeout expires; its goroutine keeps running and is a documented leak the caller accepts.
- Panic values from hooks are formatted and cut to 200 bytes in the `hook_panic` error.
- There is no logging. Events are delivered to `OnEvent` synchronously.
- `Shutdown` does not call `os.Exit`, supervise a process, provide Kubernetes probe handlers, or drain gRPC servers; `Ready()` feeds probe handlers and a gRPC drain is a one-line hook.
- `HTTPServerHook` only calls `srv.Shutdown(ctx)`; server timeouts and TLS are the caller's responsibility.

## Why not signal.NotifyContext plus srv.Shutdown

That combination has no phases, no per-hook deadlines, no panic capture, no readiness flip with pre-stop delay, and no forced second signal. This package provides those behaviors with deterministic tests.

## Documentation

- [Security](docs/SECURITY.md)
- [Project record](docs/PROJECT.md)
- [Testing](docs/TESTING.md)
