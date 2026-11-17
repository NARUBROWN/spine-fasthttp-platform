package platform

import (
	"encoding/json"
	"fmt"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
	"github.com/valyala/fasthttp"
)

type responseWriter struct {
	native    *fasthttp.Response
	committed bool
}

var _ httpcontract.ResponseWriter = (*responseWriter)(nil)

func (w *responseWriter) SetHeader(key, value string) { w.native.Header.Set(key, value) }
func (w *responseWriter) AddHeader(key, value string) { w.native.Header.Add(key, value) }
func (w *responseWriter) IsCommitted() bool           { return w.committed }

func (w *responseWriter) WriteStatus(status int) error { return w.WriteBytes(status, nil) }

func (w *responseWriter) WriteJSON(status int, body json.RawMessage) error {
	w.SetHeader("Content-Type", "application/json; charset=UTF-8")
	return w.WriteBytes(status, body)
}

func (w *responseWriter) WriteString(status int, body string) error {
	w.SetHeader("Content-Type", "text/plain; charset=UTF-8")
	return w.WriteBytes(status, []byte(body))
}

func (w *responseWriter) WriteBytes(status int, body []byte) error {
	if w.committed {
		return fmt.Errorf("%w: response is already committed", httpcontract.ErrInvalidState)
	}
	if status < 200 || status > 999 {
		return fmt.Errorf("%w: fasthttp requires a final status from 200 to 999", httpcontract.ErrUnsupportedOption)
	}
	w.native.SetStatusCode(status)
	// SetBody copies the bytes: the caller may reuse them immediately.
	w.native.SetBody(body)
	w.committed = true
	return nil
}
