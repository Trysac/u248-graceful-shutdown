package gracefulshutdown

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// EventType is the type of event emitted via Options.OnEvent.
type EventType string

const (
	EventDraining     EventType = "draining"
	EventPhaseStarted EventType = "phase_started"
	EventHookDone     EventType = "hook_done"
	EventHookFailed   EventType = "hook_failed"
	EventDone         EventType = "done"
)

// Event describes a lifecycle event.
type Event struct {
	Type  string
	Hook  string
	Phase int
}

// Error is a typed error returned by the package.
type Error struct {
	Code string
	Hook string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Err }

// Options configures a Manager.
type Options struct {
	// PreStopDelay is the duration to sleep before running hooks.
	PreStopDelay time.Duration
	// Sleep is called when PreStopDelay > 0. Defaults to a select on ctx and timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// OnEvent is called synchronously for each lifecycle event.
	OnEvent func(Event)
}

// hook describes a registered shutdown hook.
type hook struct {
	name    string
	phase   int
	timeout time.Duration
	fn      func(ctx context.Context) error
}

// Manager coordinates graceful shutdown.
type Manager struct {
	mu          sync.Mutex
	opts        Options
	hooks       []hook
	hookNames   map[string]struct{}
	ready       bool
	started     bool
	done        chan struct{}
	shutdownErr error
}

// New creates a Manager with the given options.
func New(opts Options) *Manager {
	if opts.Sleep == nil {
		opts.Sleep = func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	return &Manager{
		opts:      opts,
		hookNames: make(map[string]struct{}),
		ready:     true,
		done:      make(chan struct{}),
	}
}

// Register adds a shutdown hook.
func (m *Manager) Register(name string, phase int, timeout time.Duration, fn func(ctx context.Context) error) error {
	if name == "" || fn == nil || timeout <= 0 {
		return &Error{Code: "invalid_hook", Hook: name}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return &Error{Code: "shutting_down"}
	}
	if _, exists := m.hookNames[name]; exists {
		return &Error{Code: "duplicate_hook", Hook: name}
	}
	m.hookNames[name] = struct{}{}
	m.hooks = append(m.hooks, hook{name: name, phase: phase, timeout: timeout, fn: fn})
	return nil
}

// Ready reports whether the service is accepting work.
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

// Done returns a channel closed when shutdown completes.
func (m *Manager) Done() <-chan struct{} {
	return m.done
}

// Shutdown starts the shutdown process. It is idempotent; concurrent calls block until the first completes.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		<-m.done
		m.mu.Lock()
		err := m.shutdownErr
		m.mu.Unlock()
		return err
	}
	m.started = true
	m.ready = false
	hooks := make([]hook, len(m.hooks))
	copy(hooks, m.hooks)
	m.mu.Unlock()

	defer close(m.done)

	if m.opts.OnEvent != nil {
		m.opts.OnEvent(Event{Type: string(EventDraining)})
	}
	if m.opts.PreStopDelay > 0 {
		_ = m.opts.Sleep(ctx, m.opts.PreStopDelay)
	}

	// Group hooks by phase.
	phaseMap := make(map[int][]hook)
	for _, h := range hooks {
		phaseMap[h.phase] = append(phaseMap[h.phase], h)
	}
	phases := make([]int, 0, len(phaseMap))
	for p := range phaseMap {
		phases = append(phases, p)
	}
	sort.Ints(phases)

	var hookErrors []error
	for _, phase := range phases {
		if m.opts.OnEvent != nil {
			m.opts.OnEvent(Event{Type: string(EventPhaseStarted), Phase: phase})
		}
		phaseHooks := phaseMap[phase]

		// Start all hooks in this phase concurrently.
		type resultEntry struct {
			h        hook
			resultCh chan error
			ctx      context.Context
			cancel   context.CancelFunc
		}
		results := make([]resultEntry, 0, len(phaseHooks))
		for _, h := range phaseHooks {
			hookCtx, cancel := context.WithTimeout(ctx, h.timeout)
			resultCh := make(chan error, 1)
			go func(h hook, hookCtx context.Context, resultCh chan error) {
				defer func() {
					if r := recover(); r != nil {
						msg := fmt.Sprintf("%v", r)
						if len(msg) > 200 {
							msg = msg[:200]
						}
						resultCh <- &Error{Code: "hook_panic", Hook: h.name, Err: fmt.Errorf("panic: %s", msg)}
					}
				}()
				err := h.fn(hookCtx)
				resultCh <- err
			}(h, hookCtx, resultCh)
			results = append(results, resultEntry{h: h, resultCh: resultCh, ctx: hookCtx, cancel: cancel})
		}

		// record classifies a single hook outcome. It is shared by both select
		// branches so a hook that already finished is never misreported as a
		// timeout.
		record := func(res resultEntry, err error) {
			// A hook that reports the cancellation of the shutdown context is
			// abandoned rather than failed.
			if err != nil && errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return
			}
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) && errors.Is(res.ctx.Err(), context.DeadlineExceeded) {
					err = &Error{Code: "hook_timeout", Hook: res.h.name}
				}
				hookErrors = append(hookErrors, err)
				if m.opts.OnEvent != nil {
					m.opts.OnEvent(Event{Type: string(EventHookFailed), Hook: res.h.name, Phase: phase})
				}
			} else if m.opts.OnEvent != nil {
				m.opts.OnEvent(Event{Type: string(EventHookDone), Hook: res.h.name, Phase: phase})
			}
		}

		// Wait for hooks in registration order.
		for _, res := range results {
			select {
			case err := <-res.resultCh:
				record(res, err)
			case <-res.ctx.Done():
				// The context firing and the result landing may both be ready.
				// Drain the buffered result channel before concluding a timeout so
				// a hook that already returned uses its real result.
				select {
				case err := <-res.resultCh:
					record(res, err)
				default:
					if res.ctx.Err() == context.DeadlineExceeded {
						record(res, &Error{Code: "hook_timeout", Hook: res.h.name})
					}
					// If parent canceled, we ignore; the hook is abandoned, no error/event.
				}
			}
			res.cancel()
		}

		// If context is done, mark all remaining phases as skipped.
		if ctx.Err() != nil {
			for i := sort.SearchInts(phases, phase+1); i < len(phases); i++ {
				nextPhase := phases[i]
				for _, h := range phaseMap[nextPhase] {
					err := &Error{Code: "skipped", Hook: h.name}
					hookErrors = append(hookErrors, err)
					if m.opts.OnEvent != nil {
						m.opts.OnEvent(Event{Type: string(EventHookFailed), Hook: h.name, Phase: nextPhase})
					}
				}
			}
			break
		}
	}

	if m.opts.OnEvent != nil {
		m.opts.OnEvent(Event{Type: string(EventDone)})
	}

	err := errors.Join(hookErrors...)
	m.mu.Lock()
	m.shutdownErr = err
	m.mu.Unlock()
	return err
}

// WaitForSignal waits for a signal or ctx cancellation. On first signal, it starts Shutdown with a deadline context.
// A second signal cancels the shutdown context.
func WaitForSignal(ctx context.Context, sigs <-chan os.Signal, m *Manager, deadline time.Duration) error {
	if sigs == nil {
		return fmt.Errorf("gracefulshutdown: signal channel is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sigs:
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deadline)
		defer cancel()
		shutdownDone := make(chan error, 1)
		go func() {
			shutdownDone <- m.Shutdown(shutdownCtx)
		}()
		select {
		case <-sigs: // second signal forces cancellation
			cancel()
			return <-shutdownDone
		case err := <-shutdownDone:
			return err
		}
	}
}

// HTTPServerHook returns a hook that gracefully shuts down an HTTP server.
func HTTPServerHook(srv *http.Server) func(ctx context.Context) error {
	if srv == nil {
		panic("gracefulshutdown: nil http.Server")
	}
	return func(ctx context.Context) error {
		return srv.Shutdown(ctx)
	}
}
