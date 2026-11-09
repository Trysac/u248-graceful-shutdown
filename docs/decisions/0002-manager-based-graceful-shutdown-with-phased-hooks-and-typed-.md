# Manager-based graceful shutdown with phased hooks and typed errors

Status: accepted (generated, pending human review)

## Context

A service needs deterministic shutdown: stop readiness, drain connections, close resources with deadlines, and handle a second signal for forced exit.

## Decision

Implement a Manager that registers hooks with phases and timeouts. Shutdown runs phases in ascending order, concurrently within a phase, with per-hook timeouts, panic recovery, and skipped detection for later phases after context cancellation.

## Consequences

Callers get a reusable, testable component. Hooks that ignore context may leak goroutines (documented). Type errors allow error inspection. Signal handling is extracted into WaitForSignal.
