package platform

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
)

func TestBodyLimitFixedAndChunked(t *testing.T) {
	for _, max := range []int64{4, -1} {
		for _, chunked := range []bool{false, true} {
			for _, body := range []string{"abcd", "abcde"} {
				a := configured(t, httpcontract.Config{Options: httpcontract.ServerOptions{MaxBodyBytes: max}, Execute: func(c httpcontract.HTTPContext) error {
					body, err := io.ReadAll(c.Body())
					if err != nil {
						var requestErr *httpcontract.RequestError
						var cause *http.MaxBytesError
						if !errors.As(err, &requestErr) || !errors.As(err, &cause) || requestErr.Status != 413 {
							t.Errorf("limit error lost its cause: %v", err)
						}
						return c.Writer().WriteStatus(413)
					}
					return c.Writer().WriteBytes(200, body)
				}})
				address := managed(t, a)
				request, _ := http.NewRequest("POST", "http://"+address+"/", strings.NewReader(body))
				if chunked {
					request.ContentLength = -1
				}
				client := &http.Client{Timeout: 5 * time.Second}
				t.Cleanup(client.CloseIdleConnections)
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				want := 200
				if max > 0 && int64(len(body)) > max {
					want = 413
				}
				if response.StatusCode != want {
					t.Fatalf("limit=%d chunked=%t size=%d: %d", max, chunked, len(body), response.StatusCode)
				}
			}
		}
	}
}

func TestNativeTruncatedStreamsAndKeepAlive(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		broken      bool
	}{
		{"content-length", "Content-Length: 5000\r\n\r\n" + strings.Repeat("a", 4097), true},
		{"chunk-terminator", "Transfer-Encoding: chunked\r\n\r\n2\r\nab\r\n", true},
		{"trailer-terminator", "Transfer-Encoding: chunked\r\n\r\n2\r\nab\r\n0\r\n", true},
		{"complete-chunk", "Transfer-Encoding: chunked\r\n\r\n2\r\nab\r\n0\r\n\r\n", false},
		{"complete-trailer", "Transfer-Encoding: chunked\r\nTrailer: X-End\r\n\r\n2\r\nab\r\n0\r\nX-End: yes\r\n\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readDone := make(chan error, 2)
			a := configured(t, httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error {
				_, err := io.ReadAll(c.Body())
				if err == nil {
					for range 2 {
						_, repeated := c.Body().Read(make([]byte, 1))
						if repeated != io.EOF {
							err = errors.New("repeated reads after EOF did not return EOF")
						}
					}
				}
				readDone <- err
				if err != nil {
					return c.Writer().WriteStatus(400)
				}
				return c.Writer().WriteString(200, "ok")
			}})
			address := managed(t, a)
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\n"+tc.frame)
			if err != nil {
				t.Fatal(err)
			}
			if tc.broken {
				_ = conn.(*net.TCPConn).CloseWrite()
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			readErr := receive(t, readDone)
			if tc.broken && (readErr == nil || readErr == io.EOF || response.StatusCode != 400) || !tc.broken && (readErr != nil || response.StatusCode != 200) {
				t.Fatalf("stream truncation: read=%v status=%d", readErr, response.StatusCode)
			}
			if !tc.broken {
				_, _ = io.WriteString(conn, "GET /next HTTP/1.1\r\nHost: localhost\r\n\r\n")
				next, err := http.ReadResponse(reader, nil)
				if err != nil {
					t.Fatalf("keep-alive reuse: %v", err)
				}
				_, _ = io.Copy(io.Discard, next.Body)
				_ = next.Body.Close()
				if err := receive(t, readDone); err != nil || next.StatusCode != 200 {
					t.Fatalf("next request: %v status=%d", err, next.StatusCode)
				}
			}
		})
	}
}

type observedReader struct {
	reads int
	err   error
}

func (r *observedReader) Read([]byte) (int, error) { r.reads++; return 0, r.err }

func TestBodyGetterIsLazyAndPreservesIOErrors(t *testing.T) {
	cause := errors.New("source failure")
	source := &observedReader{err: cause}
	c := &requestContext{body: &requestReader{source: source, length: -1, limit: 4, remaining: 4}}
	_ = c.Body()
	_ = c.Body()
	if source.reads != 0 {
		t.Fatal("getter read the source")
	}
	_, err := c.Body().Read(make([]byte, 8))
	var requestErr *httpcontract.RequestError
	if !errors.Is(err, cause) || errors.As(err, &requestErr) {
		t.Fatalf("ordinary I/O error was replaced with an HTTP input error: %v", err)
	}
}
