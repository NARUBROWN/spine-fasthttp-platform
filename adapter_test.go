package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
	"github.com/valyala/fasthttp"
)

func configured(t *testing.T, cfg httpcontract.Config, options ...FastHTTPOption) *FastHTTPAdapter {
	t.Helper()
	a, err := NewFactory(options...).New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a.(*FastHTTPAdapter)
}

func nativeRequest(method, target, body string) *fasthttp.RequestCtx {
	var request fasthttp.Request
	request.Header.SetMethod(method)
	request.SetRequestURI(target)
	request.Header.SetHost("example.test:8080")
	request.SetBodyString(body)
	var native fasthttp.RequestCtx
	native.Init(&request, nil, nil)
	return &native
}

func managed(t *testing.T, a *FastHTTPAdapter, options ...httpcontract.ServeOption) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Serve(listener, options...) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := receive(t, done); !errors.Is(err, httpcontract.ErrServerClosed) {
			t.Errorf("Serve shutdown: %v", err)
		}
	})
	return listener.Addr().String()
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test event")
		var zero T
		return zero
	}
}

func TestFactoryValidationAndCapabilities(t *testing.T) {
	f := NewFactory()
	caps := f.Capabilities()
	if caps.ContractVersion != httpcontract.ContractVersion || !caps.TLS || caps.NetHTTPHandler || caps.NetHTTPUpgrade || caps.ClientDisconnectCancellation {
		t.Fatalf("capabilities: %+v", caps)
	}
	for _, requirement := range []httpcontract.Requirements{
		{NetHTTPHandler: true}, {NetHTTPUpgrade: true}, {ClientDisconnectCancellation: true},
	} {
		if err := httpcontract.ValidateFactory(f, requirement); !errors.Is(err, httpcontract.ErrUnsupportedCapability) {
			t.Fatalf("unsupported requirement: %v", err)
		}
	}
	for _, options := range []httpcontract.ServerOptions{
		{ReadHeaderTimeout: time.Second}, {WriteTimeout: time.Second}, {MaxHeaderBytes: 1024},
	} {
		_, err := f.New(httpcontract.Config{Execute: func(httpcontract.HTTPContext) error { return nil }, Options: options})
		if !errors.Is(err, httpcontract.ErrUnsupportedOption) {
			t.Fatalf("unsupported option %+v: %v", options, err)
		}
	}
	called := 0
	middleware := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { called++; return next }
	if _, err := NewFactory(WithMiddleware(middleware)).New(httpcontract.Config{}); !errors.Is(err, httpcontract.ErrInvalidAdapter) || called != 0 {
		t.Fatalf("validation must precede native hooks: %v, calls=%d", err, called)
	}
	for _, bad := range []FastHTTPOption{WithMiddleware(nil), WithMiddleware(func(fasthttp.RequestHandler) fasthttp.RequestHandler { return nil })} {
		_, err := NewFactory(bad).New(httpcontract.Config{Execute: func(httpcontract.HTTPContext) error { return nil }})
		if !errors.Is(err, httpcontract.ErrInvalidAdapter) {
			t.Fatalf("invalid middleware: %v", err)
		}
	}
}

func TestRawInputsSnapshotsAndRequestCompletion(t *testing.T) {
	const path = "/a%2Fb/../%252F//"
	const query = "x=%2f&x=two+words&empty="
	var saved httpcontract.HTTPContext
	a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error {
		saved = c
		if c.Method() != "POST" || c.RawPath() != path || c.RawQuery() != query || c.Header("hOsT") != "example.test:8080" {
			t.Fatalf("raw input: %s %s?%s host=%s", c.Method(), c.RawPath(), c.RawQuery(), c.Header("Host"))
		}
		if !reflect.DeepEqual(c.Headers()["X-Id"], []string{"one", "two"}) {
			t.Fatalf("repeated header: %v", c.Headers())
		}
		headers := c.Headers()
		headers["X-Id"][0] = "changed"
		delete(headers, "Host")
		if c.Header("X-ID") != "one" || c.Header("Host") == "" {
			t.Fatal("header snapshot aliases its source")
		}
		first := make([]byte, 1)
		_, _ = c.Body().Read(first)
		rest, err := io.ReadAll(c.Body())
		if err != nil || string(first)+string(rest) != "abc" {
			t.Fatalf("body getter resets position: %q %q %v", first, rest, err)
		}
		return c.Writer().WriteString(200, "ok")
	}})
	native := nativeRequest("POST", path+"?"+query, "abc")
	native.Request.Header.Add("X-ID", "one")
	native.Request.Header.Add("X-ID", "two")
	a.Handler()(native)
	if saved.Context().Err() != context.Canceled {
		t.Fatal("request context was not canceled on native return")
	}
	native.Request.Reset()
	native.Request.SetRequestURI("/replacement")
	if saved.RawPath() != path || saved.RawQuery() != query || saved.Header("X-ID") != "one" {
		t.Fatal("native reuse changed the input snapshot")
	}
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAbsoluteTargetUsesURIAuthority(t *testing.T) {
	a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error {
		if c.Header("Host") != "upstream.test:8443" || c.RawPath() != "/a%2Fb" || c.RawQuery() != "x=%2f" {
			t.Errorf("absolute target: host=%s path=%s query=%s", c.Header("Host"), c.RawPath(), c.RawQuery())
		}
		return c.Writer().WriteStatus(204)
	}})
	a.Handler()(nativeRequest("GET", "http://upstream.test:8443/a%2Fb?x=%2f", ""))
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalServerRemainsCallerOwned(t *testing.T) {
	a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error { return c.Writer().WriteString(200, "external") }})
	server := &fasthttp.Server{Handler: a.Handler(), StreamRequestBody: true, DisablePreParseMultipartForm: true, MaxRequestBodySize: 4 << 10}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.ShutdownWithContext(ctx); err != nil {
			t.Error(err)
		}
		if err := receive(t, done); err != nil {
			t.Error(err)
		}
	})
	for _, status := range []int{200, 503} {
		response, err := client.Get("http://" + listener.Addr().String() + "/")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("external server: want=%d got=%d", status, response.StatusCode)
		}
		if err := a.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedShutdownPreservesActiveResponse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error {
		close(entered)
		<-release
		return c.Writer().WriteString(200, "completed")
	}})
	address := managed(t, a)
	t.Cleanup(unblock)
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	result := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + address + "/")
		if err == nil {
			var body []byte
			body, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err == nil && (response.StatusCode != 200 || string(body) != "completed") {
				err = errors.New("shutdown lost the active response")
			}
		}
		result <- err
	}()
	receive(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown wait: %v", err)
	}
	unblock()
	if err := receive(t, result); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestResponseCopiesAndRepeatedHeaders(t *testing.T) {
	var response fasthttp.Response
	w := &responseWriter{native: &response}
	w.AddHeader("Set-Cookie", "one=1")
	w.AddHeader("Set-Cookie", "two=2")
	w.AddHeader("X-ID", "one")
	w.AddHeader("X-ID", "two")
	body := json.RawMessage(`{"ok":true}`)
	if w.IsCommitted() {
		t.Fatal("response already committed")
	}
	if err := w.WriteJSON(201, body); err != nil || !w.IsCommitted() {
		t.Fatalf("write: %v", err)
	}
	body[0] = 'x'
	cookies := 0
	response.Header.VisitAll(func(key, _ []byte) {
		if string(key) == "Set-Cookie" {
			cookies++
		}
	})
	if string(response.Body()) != `{"ok":true}` || cookies != 2 || len(response.Header.PeekAll("X-Id")) != 2 {
		t.Fatalf("response copy/headers: %q %s", response.Body(), response.Header.Header())
	}
	if err := w.WriteStatus(500); !errors.Is(err, httpcontract.ErrInvalidState) || response.StatusCode() != 201 {
		t.Fatalf("duplicate commitment: %v", err)
	}
}

func TestShutdownWaitsForNativePostProcessing(t *testing.T) {
	entered, release, returned := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var current context.Context
	order := []string{}
	a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error {
		current = c.Context()
		order = append(order, "execute")
		return c.Writer().WriteStatus(204)
	}}, WithMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(native *fasthttp.RequestCtx) {
			order = append(order, "outer-in")
			next(native)
			entered <- current
			<-release
			order = append(order, "outer-out")
		}
	}))
	go func() { a.Handler()(nativeRequest("GET", "/", "")); close(returned) }()
	active := receive(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Shutdown(ctx); !errors.Is(err, context.Canceled) || active.Err() != nil {
		t.Fatalf("shutdown must preserve active postprocessing: %v, %v", err, active.Err())
	}
	blocked := nativeRequest("GET", "/", "")
	a.Handler()(blocked)
	if blocked.Response.StatusCode() != 503 {
		t.Fatal("new request was accepted after stopping")
	}
	unblock()
	receive(t, returned)
	if err := a.Shutdown(t.Context()); err != nil || active.Err() != context.Canceled {
		t.Fatalf("repeated wait: %v, %v", err, active.Err())
	}
	if !reflect.DeepEqual(order, []string{"outer-in", "execute", "outer-out"}) {
		t.Fatalf("middleware boundary: %v", order)
	}
}

func TestConcurrentRequestsOverTCP(t *testing.T) {
	a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error {
		body, err := io.ReadAll(c.Body())
		if err != nil {
			return err
		}
		c.Writer().SetHeader("X-Request-ID", c.Header("X-Request-ID"))
		return c.Writer().WriteBytes(200, body)
	}})
	address := managed(t, a)
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	results := make(chan error, 32)
	for i := range 32 {
		go func() {
			body := strings.Repeat(strconv.Itoa(i), 8192)
			req, _ := http.NewRequest("POST", "http://"+address+"/", strings.NewReader(body))
			req.Header.Set("X-Request-ID", body[:1])
			res, err := client.Do(req)
			if err == nil {
				var received []byte
				received, err = io.ReadAll(res.Body)
				_ = res.Body.Close()
				if err == nil && (res.StatusCode != 200 || string(received) != body || res.Header.Get("X-Request-ID") != body[:1]) {
					err = errors.New("request values or body leaked across concurrent requests")
				}
			}
			results <- err
		}()
	}
	for range 32 {
		if err := receive(t, results); err != nil {
			t.Error(err)
		}
	}
}

type closeCountingListener struct {
	net.Listener
	closes int
	err    error
}

func (l *closeCountingListener) Close() error { l.closes++; return l.err }

func TestServeOwnsRejectedListeners(t *testing.T) {
	a := configured(t, httpcontract.Config{Execute: func(httpcontract.HTTPContext) error { return nil }})
	a.StopAccepting()
	cause := errors.New("close failure")
	listener := &closeCountingListener{err: cause}
	err := a.Serve(listener)
	if !errors.Is(err, httpcontract.ErrInvalidState) || !errors.Is(err, cause) || listener.closes != 1 {
		t.Fatalf("listener ownership: %v, closes=%d", err, listener.closes)
	}
	var nilListener *net.TCPListener
	if err := a.Serve(nilListener); !errors.Is(err, httpcontract.ErrInvalidState) {
		t.Fatalf("typed nil listener: %v", err)
	}
	if err := a.Shutdown(nil); !errors.Is(err, httpcontract.ErrInvalidState) {
		t.Fatalf("nil shutdown context: %v", err)
	}
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestServeShutdownStartupRaceAndNoRestart(t *testing.T) {
	for range 20 {
		a := configured(t, httpcontract.Config{Execute: func(httpcontract.HTTPContext) error { return nil }})
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- a.Serve(listener) }()
		if err := a.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := receive(t, done); !errors.Is(err, httpcontract.ErrServerClosed) && !errors.Is(err, httpcontract.ErrInvalidState) {
			t.Fatalf("startup race: %v", err)
		}
		other, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Serve(other); !errors.Is(err, httpcontract.ErrInvalidState) {
			t.Fatalf("restart accepted: %v", err)
		}
		if conn, err := net.DialTimeout("tcp", other.Addr().String(), time.Second); err == nil {
			_ = conn.Close()
			t.Fatal("rejected listener left open")
		}
	}
}
