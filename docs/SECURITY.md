# Security notes

## Threat model

`github.com/Trysac/u248-graceful-shutdown` is a Go library. It uses the standard library only and has no registry token, credential store, command execution, or logging. It accepts caller-supplied hooks, a signal channel, an HTTP server, and an event callback. The security boundary is the caller's process and hook code.

Defended:

- Hook panics are recovered and reported as `*Error` with code `hook_panic` and the hook name. Panic values are formatted and cut to 200 bytes, so the library does not propagate hook panics as process panics.
- Per-hook timeouts: each hook runs with `context.WithTimeout(shutdownCtx, timeout)`; a phase ends when every hook returned or timed out. A context-respecting hook cannot block shutdown forever.
- Shutdown context cancellation: after a phase, if the shutdown context is done, later hooks are marked `skipped` and are not called. The first shutdown result is stored; later calls return the same error.
- `Register` validation: an empty name, nil function, or timeout `<= 0` returns `invalid_hook`; a repeated name returns `duplicate_hook`; registration after shutdown started returns `shutting_down`. Negative phase numbers are allowed and ordered before `0`.
- `HTTPServerHook` uses `srv.Shutdown(ctx)`, so in-flight requests can complete while the server stops accepting new connections within the caller's context.
- `WaitForSignal` does not call `os.Exit`; it starts `Shutdown` under `context.WithTimeout(context.WithoutCancel(ctx), deadline)`, and a second signal cancels that context.

Not defended:

- Hooks that ignore their context are abandoned; the goroutine keeps running and is a documented leak the caller accepts. Timeouts cannot kill goroutines or roll back side effects.
- Hook code and its I/O are caller-controlled; the package does not sandbox hooks, validate phase semantics beyond ordering, or undo hook side effects.
- Panic recovery applies to hooks. Panics in `OnEvent`, `Sleep`, or caller signal-channel handling are not described as recovered.
- No logging. `OnEvent` runs synchronously and never receives hook error text; event callbacks must be safe for the shutdown goroutine.
- No process supervision, forced process exit, Kubernetes probe handlers, gRPC draining, signal registration, TLS configuration, or server timeout configuration. The caller must wire signals with `signal.Notify` and supply its own `*http.Server`.
- No secret storage or network I/O beyond `net/http` types used by `HTTPServerHook`.

## Publishing

The package is distributed as a Go module; release tags are served through the Go module proxy and checksum database. No registry token is stored in the repository.

To report a vulnerability, follow the repository's security policy.
