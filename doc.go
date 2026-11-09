// Package gracefulshutdown is a small, dependency-free Go library: Ordered graceful shutdown for Go services: readiness flip, pre-stop delay, phased hooks with per-hook timeouts, panic capture, second-signal force and HTTP draining
//
// The public API is every exported identifier of this root package. Functions are pure: they do
// no I/O, keep no global mutable state and report invalid input through returned errors.
package gracefulshutdown
