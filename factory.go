// Package platform implements the Spine HTTP v3 contract with fasthttp.
package platform

import (
	"fmt"
	"reflect"
	"slices"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
	"github.com/valyala/fasthttp"
)

type factory struct {
	middleware []func(fasthttp.RequestHandler) fasthttp.RequestHandler
}

// FastHTTPOption configures the native request pipeline.
type FastHTTPOption func(*factory)

// WithMiddleware places the first middleware at the outside of the pipeline.
// Middleware constructors run once per adapter, after configuration validation.
func WithMiddleware(middleware ...func(fasthttp.RequestHandler) fasthttp.RequestHandler) FastHTTPOption {
	copy := slices.Clone(middleware)
	return func(f *factory) { f.middleware = append(f.middleware, copy...) }
}

// NewFactory prepares a factory without opening listeners or starting goroutines.
func NewFactory(options ...FastHTTPOption) httpcontract.Factory {
	f := factory{}
	for _, option := range options {
		if option != nil {
			option(&f)
		}
	}
	f.middleware = slices.Clone(f.middleware)
	return f
}

var _ httpcontract.Factory = factory{}

func (factory) Capabilities() httpcontract.Capabilities {
	return httpcontract.Capabilities{
		ContractVersion: httpcontract.ContractVersion,
		TLS:             true,
		ServerOptions: httpcontract.ServerOptionSupport{
			DisableRecover: true, ReadTimeout: true, IdleTimeout: true,
		},
	}
}

func (f factory) New(config httpcontract.Config) (httpcontract.HTTPAdapter, error) {
	if err := config.Validate(f.Capabilities()); err != nil {
		return nil, err
	}
	for i, middleware := range f.middleware {
		if middleware == nil {
			return nil, fmt.Errorf("%w: middleware[%d] is nil", httpcontract.ErrInvalidAdapter, i)
		}
	}
	s := newAdapter(config)
	handler := fasthttp.RequestHandler(s.executeRequest)
	for _, middleware := range slices.Backward(f.middleware) {
		handler = middleware(handler)
		if handler == nil {
			return nil, fmt.Errorf("%w: middleware returned a nil handler", httpcontract.ErrInvalidAdapter)
		}
	}
	s.native = handler
	return s, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
