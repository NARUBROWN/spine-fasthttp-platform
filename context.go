package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
	"github.com/valyala/fasthttp"
)

type requestContext struct {
	ctx                 context.Context
	method, path, query string
	headers             http.Header
	body                io.Reader
	writer              httpcontract.ResponseWriter
}

var _ httpcontract.HTTPContext = (*requestContext)(nil)

func newContext(native *fasthttp.RequestCtx, ctx context.Context, max int64) *requestContext {
	uri := native.URI()
	headers := make(http.Header)
	native.Request.Header.VisitAll(func(key, value []byte) { headers.Add(string(key), string(value)) })
	// Absolute-form targets use their URI authority, as required by HTTP.
	headers.Set("Host", string(uri.Host()))
	var source io.Reader = native.Request.BodyStream()
	if source == nil {
		source = bytes.NewReader(native.Request.Body())
	}
	reader := &requestReader{source: source, length: int64(native.Request.Header.ContentLength()), remaining: max, limit: max}
	return &requestContext{
		ctx: ctx, method: string(native.Method()), path: string(uri.PathOriginal()),
		query: string(uri.QueryString()), headers: headers, body: reader,
		writer: &responseWriter{native: &native.Response},
	}
}

func (c *requestContext) Context() context.Context            { return c.ctx }
func (c *requestContext) Method() string                      { return c.method }
func (c *requestContext) RawPath() string                     { return c.path }
func (c *requestContext) RawQuery() string                    { return c.query }
func (c *requestContext) Header(name string) string           { return c.headers.Get(name) }
func (c *requestContext) Headers() map[string][]string        { return c.headers.Clone() }
func (c *requestContext) Body() io.Reader                     { return c.body }
func (c *requestContext) Writer() httpcontract.ResponseWriter { return c.writer }

type requestReader struct {
	source                   io.Reader
	length, remaining, limit int64
	read                     int64
	err                      error
}

func (r *requestReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.limit > 0 && r.remaining < int64(len(p)) {
		p = p[:int(r.remaining)+1]
	}
	n, err := r.source.Read(p)
	r.read += int64(n)
	if err == io.EOF && r.length >= 0 && r.read < r.length {
		err = io.ErrUnexpectedEOF
	}
	if r.limit > 0 {
		if int64(n) > r.remaining {
			n = int(r.remaining)
			r.remaining = 0
			var cause error = &http.MaxBytesError{Limit: r.limit}
			if err != nil && err != io.EOF {
				cause = errors.Join(cause, err)
			}
			r.err = &httpcontract.RequestError{Status: http.StatusRequestEntityTooLarge, Cause: cause}
			return n, r.err
		}
		r.remaining -= int64(n)
	}
	if err != nil {
		r.err = err
	}
	return n, err
}
