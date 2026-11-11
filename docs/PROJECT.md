# Project record

## Objective

Ordered graceful shutdown for Go services: readiness flip, pre-stop delay, phased hooks with per-hook timeouts, panic capture, second-signal force and HTTP draining.

## Scope

- In scope: `Options`, `New`, `Manager.Register`, `Manager.Shutdown`, `Manager.Ready`, `Manager.Done`, `WaitForSignal`, `HTTPServerHook`, `Event`, and `Error`.
- In scope: shutdown phases in ascending order; hooks within a phase concurrent; per-hook `context.WithTimeout`; `errors.Join` in registration order; stored shutdown result.
- In scope: readiness flip, `PreStopDelay` with injectable `Sleep`, `OnEvent` events, panic recovery with `hook_panic`, skipped hooks after shutdown-context cancellation, second-signal cancellation in `WaitForSignal`.
- In scope tests: shutdown with no hooks, negative phase numbers ordered before `0`, nil `OnEvent`, and `Ready()` read concurrently with `Shutdown`.
- In scope: standard library only.

## Out of scope

- Calling `os.Exit`.
- Process supervision.
- Kubernetes probes handlers; `Ready()` feeds them.
- Draining gRPC servers; that is a one-line hook.
- Third-party dependencies.

## Timeline

| Date | Event |
|---|---|
| 2026 | Repository created from the factory utility template. |

## Effort metrics

Measured values only; a value that has not been measured stays "pending measurement".

| Metric | Value |
|---|---|
| Tokens (input / output) | 810621 / 95316 |
| Hours (human) | pending measurement |
| Cost (USD) | 0.1319 |

## Factory run u248 (measured from API usage)

| Phase | Model | Batch | Input | Output | Cache write | Cache read | Cost (USD) |
|---|---|---|---|---|---|---|---|
| generation | deepseek-v4-pro | yes | 3524 | 17186 | 0 | 1280 | 0.0364 |
| generation | deepseek-v4-pro | yes | 68 | 27399 | 0 | 4736 | 0.0544 |
| fix-1 | deepseek-flash | no | 18860 | 9979 | 0 | 344064 | 0.0098 |
| audit | deepseek-flash | no | 24489 | 22158 | 0 | 343936 | 0.0180 |
| fix-2 | deepseek-flash | no | 9244 | 4063 | 0 | 56192 | 0.0040 |
| docs | deepseek-flash | no | 3844 | 14531 | 0 | 384 | 0.0093 |

Total: $0.1319. Prices: deepseek line, as measured by its adapter.
