package platform

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
	"github.com/valyala/fasthttp"
)

type lifecycleState uint8

const (
	stateReady lifecycleState = iota
	stateServing
	stateStopping
	stateStopped
)

// FastHTTPAdapter owns a managed server and tracks requests on both managed and
// external servers. Construct it through NewFactory; its zero value is invalid.
type FastHTTPAdapter struct {
	execute      httpcontract.ExecuteFunc
	native       fasthttp.RequestHandler
	server       *fasthttp.Server
	maxBodyBytes int64
	serveOptions httpcontract.ServeOptions
	mu           sync.Mutex
	state        lifecycleState
	active       int
	listener     *ownedListener
	drained      chan struct{}
	serveDone    chan struct{}
	shutdownDone chan struct{}
	shutdownOnce sync.Once
	shutdownErr  error
}

var _ httpcontract.HTTPAdapter = (*FastHTTPAdapter)(nil)

func newAdapter(config httpcontract.Config) *FastHTTPAdapter {
	opts := config.Options
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = 32 << 20
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 30 * time.Second
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 120 * time.Second
	}
	s := &FastHTTPAdapter{
		execute: config.Execute, maxBodyBytes: opts.MaxBodyBytes,
		serveOptions: config.Serve.Clone(),
		drained:      make(chan struct{}), serveDone: make(chan struct{}), shutdownDone: make(chan struct{}),
	}
	s.server = &fasthttp.Server{
		Handler: s.handle, ReadTimeout: opts.ReadTimeout, IdleTimeout: opts.IdleTimeout,
		StreamRequestBody: true, DisablePreParseMultipartForm: true,
		// With streaming enabled this is a bounded prefetch threshold, not the
		// total body limit. The contract reader enforces MaxBodyBytes lazily.
		MaxRequestBodySize: 4 << 10,
	}
	return s
}

// Handler returns the same protected native entry point used by Serve.
// The caller owns the external fasthttp.Server and its shutdown.
func (s *FastHTTPAdapter) Handler() fasthttp.RequestHandler {
	if s == nil || s.native == nil {
		return nil
	}
	return s.handle
}

// Serve owns the listener on every return path and may be called only once.
func (s *FastHTTPAdapter) Serve(listener net.Listener, options ...httpcontract.ServeOption) (serveErr error) {
	if isNil(listener) {
		return fmt.Errorf("%w: listener is nil", httpcontract.ErrInvalidState)
	}
	owned := &ownedListener{Listener: listener, started: make(chan struct{})}
	defer func() { serveErr = errors.Join(serveErr, owned.Close()) }()
	if s == nil || s.server == nil || s.native == nil {
		return fmt.Errorf("%w: FastHTTPAdapter is not initialized", httpcontract.ErrInvalidAdapter)
	}
	resolved, err := s.serveOptions.Resolve((factory{}).Capabilities(), options...)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.state != stateReady {
		s.mu.Unlock()
		return fmt.Errorf("%w: Serve requires a new adapter", httpcontract.ErrInvalidState)
	}
	s.state, s.listener = stateServing, owned
	s.mu.Unlock()
	defer close(s.serveDone)
	var native net.Listener = owned
	if resolved.TLS != nil {
		native = tls.NewListener(owned, resolved.TLS)
	}
	err = s.server.Serve(native)
	s.mu.Lock()
	stopping := s.state >= stateStopping
	s.mu.Unlock()
	if stopping && (err == nil || errors.Is(err, net.ErrClosed)) {
		return httpcontract.ErrServerClosed
	}
	if err == nil {
		return fmt.Errorf("%w: unexpected nil Serve result", httpcontract.ErrInvalidAdapter)
	}
	return err
}

// The first Accept signals that fasthttp has registered its listener. Shutdown
// waits for this signal to avoid racing with native server initialization.
type ownedListener struct {
	net.Listener
	started   chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	err       error
}

func (l *ownedListener) Accept() (net.Conn, error) {
	l.startOnce.Do(func() { close(l.started) })
	return l.Listener.Accept()
}

func (l *ownedListener) Close() error {
	l.closeOnce.Do(func() { l.err = l.Listener.Close() })
	return l.err
}

type requestContextKey struct{}

func (s *FastHTTPAdapter) handle(native *fasthttp.RequestCtx) {
	s.mu.Lock()
	if s.state >= stateStopping {
		s.mu.Unlock()
		native.Error("application is closing", fasthttp.StatusServiceUnavailable)
		return
	}
	s.active++
	s.mu.Unlock()
	// RequestCtx is pooled and its Done channel represents server shutdown,
	// rather than request completion. Keep it out of the standard context.
	ctx, cancel := context.WithCancel(context.Background())
	native.SetUserValue(requestContextKey{}, ctx)
	defer func() {
		cancel()
		native.RemoveUserValue(requestContextKey{})
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active--
		if s.state >= stateStopping && s.active == 0 {
			close(s.drained)
		}
	}()
	s.native(native)
}

func (s *FastHTTPAdapter) executeRequest(native *fasthttp.RequestCtx) {
	ctx := native.UserValue(requestContextKey{}).(context.Context)
	// Execute owns the response, including its error response.
	_ = s.execute(newContext(native, ctx, s.maxBodyBytes))
}

// StopAccepting synchronously rejects new native entries with HTTP 503.
func (s *FastHTTPAdapter) StopAccepting() {
	if s == nil || s.server == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state >= stateStopping {
		return
	}
	s.state = stateStopping
	if s.active == 0 {
		close(s.drained)
	}
}

// Shutdown starts cleanup once. Its context limits only this wait; cleanup
// continues and another caller may wait again with a new context.
func (s *FastHTTPAdapter) Shutdown(ctx context.Context) error {
	if isNil(ctx) {
		return fmt.Errorf("%w: shutdown context is nil", httpcontract.ErrInvalidState)
	}
	if s == nil || s.server == nil {
		return fmt.Errorf("%w: FastHTTPAdapter is not initialized", httpcontract.ErrInvalidAdapter)
	}
	s.StopAccepting()
	s.shutdownOnce.Do(func() {
		go func() {
			s.mu.Lock()
			listener := s.listener
			s.mu.Unlock()
			if listener != nil {
				// Native Shutdown cannot be called before Serve registers its listener.
				<-listener.started
				s.shutdownErr = s.server.Shutdown()
				<-s.serveDone
			}
			<-s.drained
			s.mu.Lock()
			s.state = stateStopped
			s.mu.Unlock()
			close(s.shutdownDone)
		}()
	})
	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	default:
	}
	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
