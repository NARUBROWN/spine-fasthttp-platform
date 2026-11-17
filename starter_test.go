package platform_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NARUBROWN/spine"
	platform "github.com/NARUBROWN/spine-fasthttp-platform"
	httpcontract "github.com/NARUBROWN/spine/contract/http"
	"github.com/NARUBROWN/spine/pkg/api"
	"github.com/NARUBROWN/spine/pkg/boot"
	"github.com/NARUBROWN/spine/pkg/constructor"
	"github.com/NARUBROWN/spine/pkg/httpx"
	"github.com/NARUBROWN/spine/pkg/input"
)

type echoDTO struct {
	Name string `json:"name"`
}

type echoQuery struct {
	Query string `query:"query"`
}

type echoHeader struct {
	Header string `header:"Header"`
}

type echoController struct{}

func (*echoController) Constructor() *echoController { return &echoController{} }

func (*echoController) RegisterRoutes(r *api.Registry) {

	r.POST("/echo/:id", func(body input.Body[echoDTO], path input.Path[int], query input.Query[echoQuery], header input.Header[echoHeader]) httpx.Response[map[string]any] {
		return httpx.Response[map[string]any]{Body: map[string]any{"name": body.Name, "id": *path, "query": query.Query, "header": header.Header}}
	})

	r.GET("/hello", func() httpx.Response[string] {
		return httpx.Response[string]{Body: "hello"}
	})

}

func TestPublicStarterOverTCPAndRuntimeOpenAPI(t *testing.T) {
	app := platform.New()
	app.Constructor(constructor.Controller(&echoController{}))
	prepared, err := app.Prepare(t.Context(), boot.Options{HTTP: &boot.HTTPOptions{MaxBodyBytes: 128, OpenAPI: boot.OpenAPIOptions{Enabled: true, Title: "FastHTTP", Version: "preview"}}})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- prepared.HTTPAdapter().Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := prepared.Close(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, httpcontract.ErrServerClosed) {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("Serve did not stop")
		}
	})
	if _, err := spine.NetHTTPHandler(prepared); !errors.Is(err, httpcontract.ErrUnsupportedCapability) {
		t.Fatalf("net/http capability must be rejected: %v", err)
	}
	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/hello", "", 200},
		{"HEAD", "/hello", "", 404},
		{"POST", "/echo/42?query=two+words", `{"name":"fasthttp"}`, 200},
		{"POST", "/echo/42", `{"name":`, 400},
		{"POST", "/echo/42", strings.Repeat("x", 129), 413},
		{"GET", "/openapi.json", "", 200},
		{"HEAD", "/openapi.json", "", 200},
		{"GET", "/missing", "", 404},
	} {
		request, _ := http.NewRequest(tc.method, base+tc.path, strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Header", "from-client")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != tc.status {
			t.Fatalf("%s %s: status=%d body=%s err=%v", tc.method, tc.path, response.StatusCode, body, err)
		}
		if tc.method == "HEAD" && len(body) != 0 {
			t.Fatal("HEAD response has a body")
		}
		if tc.method == "POST" && tc.status == 200 {
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil || got["name"] != "fasthttp" || got["id"] != float64(42) || got["query"] != "two words" || got["header"] != "from-client" {
				t.Fatalf("binding: %s, %v", body, err)
			}
		}
		if tc.method == "GET" && tc.path == "/openapi.json" && !strings.Contains(string(body), `"/echo/{id}"`) {
			t.Fatalf("runtime OpenAPI does not describe registered routes: %s", body)
		}
	}
}
