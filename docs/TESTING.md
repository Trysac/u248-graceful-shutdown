# Testing

## Running

```sh
go test ./...
go test -race ./...
```

`go test -race` must pass.

## Policy

- Tests exercise the public API in package `gracefulshutdown_test`.
- Tests are deterministic: no network, no wall clock, no dependence on test order. Real timers appear only in timeout tests, with 20 ms timeouts, 1 s bounds, and a 50x margin.
- Each bug fix starts with a failing test that reproduces it.
- The suite must cover the shutdown behavior and edge cases listed below.
- No individual test file names are given in the supplied code snapshot; test files use Go's `*_test.go` convention beside the package.

## Coverage

- Phase order: with `http` (phase 1), `cache` (phase 1), and `db` (phase 2), `Shutdown(ctx)` starts `http` and `cache` before `db`; `db` starts only after both phase 1 hooks return; `Shutdown` returns `nil`.
- Readiness and pre-stop: `Ready()` is true before `Shutdown`; with `PreStopDelay` `5 * time.Second` and an injected `Sleep`, `Ready()` is already false when `Sleep` is called, and `Sleep` receives exactly `5 * time.Second`.
- Errors and panics: if `http` returns `errBoom` and `db` panics with `kaboom`, `Shutdown` returns an error where `errors.Is(err, errBoom)` is true and `errors.As` finds an `*Error` with code `hook_panic` and hook `db`; joined errors keep registration order.
- Per-hook timeout: a hook `stuck` with timeout `20 * time.Millisecond` that ignores its context makes `Shutdown` report an `*Error` with code `hook_timeout` and hook `stuck`; the next phase still runs; `Shutdown` returns in less than `1 * time.Second`.
- Shutdown-context cancellation: when the context passed to `Shutdown` is cancelled during phase 1, phase 2 hooks are never called and each is reported as an `*Error` with code `skipped`.
- Registration and repeat calls: a second `Shutdown` call made while the first runs blocks until it finishes and returns the same error; `Register` after shutdown started returns code `shutting_down`; an empty name, a nil function, or a timeout `<= 0` returns code `invalid_hook`; a repeated name returns code `duplicate_hook`.
- Signals: `WaitForSignal(ctx, sigs, m, 30 * time.Second)` runs `Shutdown` after one value on `sigs`; when a hook blocks until its context is done, a second value on `sigs` cancels the shutdown context and `WaitForSignal` returns in less than `1 * time.Second`; if `ctx` is cancelled before any signal it returns `ctx.Err()` without calling `Shutdown`.
- HTTP draining: with `HTTPServerHook(srv)` registered, an in-flight request started before `Shutdown` completes with status 200 and `srv.Serve(listener)` returns `http.ErrServerClosed`.
- Edge cases: shutdown with no hooks, negative phase numbers ordered before `0`, nil `OnEvent`, and `Ready()` read concurrently with `Shutdown`.

## Tiers

- Local: public-API behavior tests, including the timeout tests that use real timers, and the race detector run with `go test -race`.
- No external-tool conformance tier is described by the package specification; the package uses the Go standard library only.

## Layout

Test files use Go's `<file>_test.go` convention beside the package. No separate fixtures or testdata files are described in the supplied specification.
