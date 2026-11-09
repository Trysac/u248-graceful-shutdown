package gracefulshutdown_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Trysac/u248-graceful-shutdown"
)

func TestOrderPhases(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	var mu sync.Mutex
	var order []string
	wg := &sync.WaitGroup{}
	wg.Add(2)

	register := func(name string, phase int, fn func(context.Context) error) {
		if err := m.Register(name, phase, time.Second, fn); err != nil {
			t.Fatalf("Register(%s) error = %v", name, err)
		}
	}

	register("http", 1, func(ctx context.Context) error {
		mu.Lock()
		order = append(order, "http")
		mu.Unlock()
		wg.Done()
		return nil
	})
	register("cache", 1, func(ctx context.Context) error {
		mu.Lock()
		order = append(order, "cache")
		mu.Unlock()
		wg.Done()
		return nil
	})
	register("db", 2, func(ctx context.Context) error {
		wg.Wait()
		mu.Lock()
		order = append(order, "db")
		mu.Unlock()
		return nil
	})

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}

	var httpIdx, cacheIdx, dbIdx int
	for i, v := range order {
		switch v {
		case "http":
			httpIdx = i
		case "cache":
			cacheIdx = i
		case "db":
			dbIdx = i
		}
	}
	if dbIdx <= httpIdx || dbIdx <= cacheIdx {
		t.Fatalf("db (%d) should start after http (%d) and cache (%d)", dbIdx, httpIdx, cacheIdx)
	}
}

func TestPreStopDelayReadyFalseAndDuration(t *testing.T) {
	var m *gracefulshutdown.Manager
	var sleepCalled bool
	var sleepDuration time.Duration
	var sawReadyFalse bool

	opts := gracefulshutdown.Options{
		PreStopDelay: 5 * time.Second,
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleepCalled = true
			sleepDuration = d
			if m != nil && !m.Ready() {
				sawReadyFalse = true
			}
			return nil
		},
	}
	m = gracefulshutdown.New(opts)
	if !m.Ready() {
		t.Fatal("Ready should be true initially")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}
	if !sleepCalled {
		t.Fatal("Sleep not called")
	}
	if sleepDuration != 5*time.Second {
		t.Fatalf("Sleep duration = %v, want %v", sleepDuration, 5*time.Second)
	}
	if !sawReadyFalse {
		t.Fatal("Ready() was true during Sleep")
	}
	if m.Ready() {
		t.Fatal("Ready should be false after shutdown")
	}
}

func TestPanicAndErrorJoin(t *testing.T) {
	errBoom := errors.New("boom")
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	if err := m.Register("http", 1, time.Second, func(ctx context.Context) error { return errBoom }); err != nil {
		t.Fatalf("Register http error = %v", err)
	}
	if err := m.Register("db", 1, time.Second, func(ctx context.Context) error { panic("kaboom") }); err != nil {
		t.Fatalf("Register db error = %v", err)
	}

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("errors.Is(err, errBoom) = false, want true")
	}

	var hookPanic *gracefulshutdown.Error
	if !errors.As(err, &hookPanic) {
		t.Fatal("errors.As for panic error failed")
	}
	if hookPanic.Code != "hook_panic" || hookPanic.Hook != "db" {
		t.Fatalf("panic error = %+v, want Code=hook_panic Hook=db", hookPanic)
	}

	// Check join order.
	type unwrapper interface {
		Unwrap() []error
	}
	ue, ok := err.(unwrapper)
	if !ok {
		t.Fatal("error is not a join error")
	}
	errs := ue.Unwrap()
	if len(errs) != 2 {
		t.Fatalf("joined errors = %d, want 2", len(errs))
	}
	if !errors.Is(errs[0], errBoom) {
		t.Fatal("first joined error is not errBoom")
	}
	var secondPanic *gracefulshutdown.Error
	if !errors.As(errs[1], &secondPanic) || secondPanic.Hook != "db" {
		t.Fatal("second joined error is not the panic error for db")
	}
}

func TestHookTimeoutAndNextPhase(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	var nextRan bool
	if err := m.Register("stuck", 1, 20*time.Millisecond, func(ctx context.Context) error { select {} }); err != nil {
		t.Fatalf("Register stuck error = %v", err)
	}
	if err := m.Register("next", 2, time.Second, func(ctx context.Context) error { nextRan = true; return nil }); err != nil {
		t.Fatalf("Register next error = %v", err)
	}

	start := time.Now()
	err := m.Shutdown(context.Background())
	elapsed := time.Since(start)
	if elapsed >= time.Second {
		t.Fatalf("Shutdown took %v, want < 1s", elapsed)
	}
	if !nextRan {
		t.Fatal("next phase hook did not run")
	}
	var timeoutErr *gracefulshutdown.Error
	if !errors.As(err, &timeoutErr) || timeoutErr.Code != "hook_timeout" || timeoutErr.Hook != "stuck" {
		t.Fatalf("error = %v, want hook_timeout for stuck", err)
	}
}

func TestReturnedHookNotReportedAsTimeout(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	releaseSlow := make(chan struct{})

	// "slow" is waited on first and blocks long enough for the later hook's
	// per-hook context to expire, even though that hook already returned nil.
	if err := m.Register("slow", 1, time.Second, func(ctx context.Context) error {
		<-releaseSlow
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("fast", 1, 20*time.Millisecond, func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		close(releaseSlow)
	}()

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown error = %v, want nil (a returned hook must not be a timeout)", err)
	}
}

func TestContextCancelSkipsLaterPhases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	started1 := make(chan struct{})
	started2 := make(chan struct{})
	phase2Called := make(chan struct{})

	if err := m.Register("p1h1", 1, time.Second, func(ctx context.Context) error { close(started1); <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("p1h2", 1, time.Second, func(ctx context.Context) error { close(started2); <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("p2", 2, time.Second, func(ctx context.Context) error { close(phase2Called); return nil }); err != nil {
		t.Fatal(err)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- m.Shutdown(ctx) }()
	<-started1
	<-started2
	cancel()
	err := <-shutdownDone

	select {
	case <-phase2Called:
		t.Fatal("phase 2 hook called, want skipped")
	case <-time.After(10 * time.Millisecond):
	}

	var skipped *gracefulshutdown.Error
	if !errors.As(err, &skipped) || skipped.Code != "skipped" || skipped.Hook != "p2" {
		t.Fatalf("error = %v, want skipped for p2", err)
	}
}

func TestSecondShutdownAndRegisterAfterStart(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	release := make(chan struct{})
	hookStarted := make(chan struct{})
	errBoom := errors.New("boom")
	if err := m.Register("block", 1, time.Second, func(ctx context.Context) error { close(hookStarted); <-release; return errBoom }); err != nil {
		t.Fatal(err)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- m.Shutdown(context.Background()) }()
	<-hookStarted

	// Register after start should return shutting_down.
	err := m.Register("late", 1, time.Second, func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatal("Register after start returned nil")
	}
	var regErr *gracefulshutdown.Error
	if !errors.As(err, &regErr) || regErr.Code != "shutting_down" {
		t.Fatalf("Register error = %v, want shutting_down", err)
	}

	// Second Shutdown should block until first completes.
	secondDone := make(chan error, 1)
	go func() { secondDone <- m.Shutdown(context.Background()) }()

	// Give the second Shutdown a chance to block.
	time.Sleep(10 * time.Millisecond)

	close(release)
	err1 := <-shutdownDone
	err2 := <-secondDone
	if !errors.Is(err1, errBoom) {
		t.Fatalf("first shutdown error = %v, want errBoom", err1)
	}
	if err1 != err2 {
		t.Fatalf("second shutdown error = %v, want same as first %v", err2, err1)
	}
}

func TestRegisterInvalidAndDuplicate(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	cases := []struct {
		name    string
		phase   int
		timeout time.Duration
		fn      func(context.Context) error
	}{
		{"", 1, time.Second, func(ctx context.Context) error { return nil }},
		{"nilfn", 1, time.Second, nil},
		{"badtimeout", 1, 0, func(ctx context.Context) error { return nil }},
	}
	for _, tc := range cases {
		err := m.Register(tc.name, tc.phase, tc.timeout, tc.fn)
		var e *gracefulshutdown.Error
		if !errors.As(err, &e) || e.Code != "invalid_hook" {
			t.Fatalf("Register(%q,%d,%v) error = %v, want invalid_hook", tc.name, tc.phase, tc.timeout, err)
		}
	}

	// Duplicate name.
	validFn := func(ctx context.Context) error { return nil }
	if err := m.Register("dup", 1, time.Second, validFn); err != nil {
		t.Fatalf("Register dup first error = %v", err)
	}
	err := m.Register("dup", 1, time.Second, validFn)
	var e *gracefulshutdown.Error
	if !errors.As(err, &e) || e.Code != "duplicate_hook" {
		t.Fatalf("Register duplicate error = %v, want duplicate_hook", err)
	}
}

func TestWaitForSignal(t *testing.T) {
	t.Run("signal then second signal cancels", func(t *testing.T) {
		m := gracefulshutdown.New(gracefulshutdown.Options{})
		hookStarted := make(chan struct{})
		if err := m.Register("block", 1, time.Minute, func(ctx context.Context) error {
			close(hookStarted)
			<-ctx.Done()
			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}

		sigs := make(chan os.Signal, 2)
		ctx := context.Background()
		done := make(chan error, 1)
		go func() { done <- gracefulshutdown.WaitForSignal(ctx, sigs, m, 30*time.Second) }()

		sigs <- os.Interrupt
		<-hookStarted
		sigs <- os.Interrupt

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("WaitForSignal error = %v, want nil", err)
			}
		case <-time.After(time.Second):
			t.Fatal("WaitForSignal did not return within 1s")
		}
	})

	t.Run("ctx canceled before signal", func(t *testing.T) {
		m := gracefulshutdown.New(gracefulshutdown.Options{})
		readyBefore := m.Ready()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		sigs := make(chan os.Signal)
		err := gracefulshutdown.WaitForSignal(ctx, sigs, m, time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitForSignal error = %v, want context.Canceled", err)
		}
		if m.Ready() != readyBefore {
			t.Fatal("Shutdown should not have been called")
		}
	})
}

func TestHTTPServerHook(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	started := make(chan struct{})
	handlerBlock := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-handlerBlock
			w.WriteHeader(http.StatusOK)
		}),
	}

	serveErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		err := srv.Serve(listener)
		serveErr <- err
	}()

	// Start request.
	clientDone := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			clientDone <- nil
			return
		}
		clientDone <- resp
	}()
	<-started

	// Register HTTP server hook and shutdown. The draining event fires after the
	// readiness flip, so releasing the in-flight handler there keeps the request
	// in flight while srv.Shutdown drains it.
	m := gracefulshutdown.New(gracefulshutdown.Options{
		OnEvent: func(ev gracefulshutdown.Event) {
			if ev.Type == "draining" {
				close(handlerBlock)
			}
		},
	})
	if err := m.Register("http", 1, time.Minute, gracefulshutdown.HTTPServerHook(srv)); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}

	// The in-flight request should complete with 200.
	resp := <-clientDone
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("request failed, response = %v", resp)
	}
	resp.Body.Close()

	// Serve should return http.ErrServerClosed.
	err = <-serveErr
	if !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve error = %v, want http.ErrServerClosed", err)
	}
	wg.Wait()
}

func TestReadyConcurrent(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = m.Ready()
			}
		}()
	}
	shutdownDone := make(chan struct{})
	go func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown error = %v", err)
		}
		close(shutdownDone)
	}()
	<-shutdownDone
	wg.Wait()
}

func TestEvents(t *testing.T) {
	var mu sync.Mutex
	var events []gracefulshutdown.Event
	m := gracefulshutdown.New(gracefulshutdown.Options{
		OnEvent: func(ev gracefulshutdown.Event) {
			// We'll capture events in a slice outside.
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	if err := m.Register("good", 1, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("bad", 1, time.Second, func(ctx context.Context) error { return errors.New("some error") }); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(context.Background()); err == nil {
		t.Fatal("expected error from bad hook")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 5 {
		t.Fatalf("events = %d, want at least 5", len(events))
	}
	if events[0].Type != "draining" {
		t.Fatalf("first event = %s, want draining", events[0].Type)
	}
	if events[len(events)-1].Type != "done" {
		t.Fatalf("last event = %s, want done", events[len(events)-1].Type)
	}
	// Check phase_started for phase 1.
	foundPhaseStarted := false
	for _, ev := range events {
		if ev.Type == "phase_started" && ev.Phase == 1 {
			foundPhaseStarted = true
			break
		}
	}
	if !foundPhaseStarted {
		t.Fatal("phase_started event not found")
	}
	// Check hook_done and hook_failed.
	countDone := 0
	countFailed := 0
	for _, ev := range events {
		switch ev.Type {
		case "hook_done":
			countDone++
		case "hook_failed":
			countFailed++
		}
	}
	if countDone != 1 || countFailed != 1 {
		t.Fatalf("hook_done=%d hook_failed=%d, want 1 each", countDone, countFailed)
	}
}

func TestNegativePhasesOrder(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	if err := m.Register("minus", -1, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("zero", 0, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("plus", 1, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	m.Shutdown(context.Background())
	// We can't capture phases directly, but the OnEvent can.
	// We'll create a new Manager with OnEvent to capture.
	var mu sync.Mutex
	var order []int
	m2 := gracefulshutdown.New(gracefulshutdown.Options{
		OnEvent: func(ev gracefulshutdown.Event) {
			if ev.Type == "phase_started" {
				mu.Lock()
				order = append(order, ev.Phase)
				mu.Unlock()
			}
		},
	})
	if err := m2.Register("minus", -1, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m2.Register("zero", 0, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m2.Register("plus", 1, time.Second, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m2.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != -1 || order[1] != 0 || order[2] != 1 {
		t.Fatalf("phase order = %v, want [-1 0 1]", order)
	}
}

func TestNoHooks(t *testing.T) {
	m := gracefulshutdown.New(gracefulshutdown.Options{})
	if !m.Ready() {
		t.Fatal("Ready should be true initially")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown error = %v, want nil", err)
	}
	if m.Ready() {
		t.Fatal("Ready should be false after shutdown")
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("Done channel should be closed")
	}
}
