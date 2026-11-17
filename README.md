English | [Korean](README.ko.md)

# Spine fasthttp Platform

Run Spine applications on fasthttp's HTTP/1.1 server. Implements the `spine.http-adapter/v3` HTTP contract. Spine core handles routing, DTO binding, validation, and OpenAPI.

## Preview installation

Requires Go 1.26.0 or later. This preview uses Spine `v0.7.6-preview.1` and fasthttp `v1.79.0`.

```sh
go get github.com/NARUBROWN/spine-fasthttp-platform@v0.1.0-preview.2
```

Generate a project with Spine CLI:

```sh
spine new fast-api --platform fasthttp --channel preview --no-interactive
```

`spine-platform.json` declares the HTTP v3 contract, the `Option` factory, and the minimum Go version for CLI generation.

## Application setup

```go
import (
    "github.com/NARUBROWN/spine"
    fasthttpPlatform "github.com/NARUBROWN/spine-fasthttp-platform"
)

app := spine.New(fasthttpPlatform.Option())
```

`fasthttpPlatform.New()` returns an application configured with the same platform. `NewFactory()` returns `contract/http.Factory` without opening a listener or starting background goroutines. See the [runnable example](examples/hello/main.go) for controller registration and startup.

```sh
go run ./examples/hello
```

The default port is 8080. Override the address with `SPINE_ADDRESS=:8081 go run ./examples/hello`.

In another terminal, send a request to receive `hello`.

```sh
curl http://localhost:8080/hello
```

## Supported behavior

| Feature | Behavior |
| --- | --- |
| Raw input | Passes the undecoded path, raw query, repeated headers, and actual authority including the port |
| Request body | Provides a streaming reader and preserves its read position across repeated getters |
| Body limit | Defaults to 32 MiB; negative values disable it. Exceeding it returns HTTP 413 `RequestError` while preserving the cause. |
| Truncated body | Does not convert fixed-length or chunked termination errors to normal EOF |
| Response | Supports JSON, strings, bytes, and repeated response headers. Supplied bytes can be reused after the write method returns. |
| Request context | Creates a standard context independent of the pooled `RequestCtx`, cancelling it after native post-processing completes |
| TLS | Supports prepared certificates and overrides through `httpcontract.WithTLS` at serving time |
| Shutdown | Rejects new requests with 503 and waits for in-flight requests and native post-processing |

Native body pre-reading is limited to 4 KiB, and multipart pre-parsing is disabled. The platform reader enforces the total body limit; Spine core owns multipart parsing and temporary file cleanup. The native server may reject network errors before parsing.

This preview does not support net/http handlers, net/http-based WebSocket upgrades, or context cancellation on client disconnect. Preparation rejects requests for those capabilities. Context derives from `context.Background()` and does not carry native `UserValue` entries. Configure panic recovery in native middleware when needed. Panics during Spine execution follow the core's recovery settings.

The response writer accepts final status codes from 200 through 999. It does not support 1xx responses or protocol upgrades. Logical response commitment does not mean the socket has finished sending.

## Server options

Pass options through `boot.HTTPOptions`. Explicit values for unsupported options return `ErrUnsupportedOption`.

| Option | Default | Support |
| --- | --- | --- |
| `MaxBodyBytes` | 32 MiB | Positive values set a limit; negative values disable it |
| `ReadTimeout` | 30 seconds | Full request read deadline |
| `IdleTimeout` | 120 seconds | Wait for the next keepalive request |
| `DisableRecover` | false | Disables Spine core recovery |
| `ReadHeaderTimeout` | 0 | Explicit values unsupported |
| `WriteTimeout` | 0 | Explicit values unsupported |
| `MaxHeaderBytes` | 0 | Explicit values unsupported |

fasthttp's `WriteTimeout` starts after the handler returns, so it is not mapped to the shared option. No native setting is advertised as equivalent to header size or header-only deadlines. The native default read buffer is 4 KiB, which also constrains header size.

## Native middleware

```go
factory := fasthttpPlatform.NewFactory(fasthttpPlatform.WithMiddleware(
    func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
        return func(ctx *fasthttp.RequestCtx) {
            ctx.Response.Header.Set("X-Service", "example")
            next(ctx)
        }
    },
))
app := spine.New(spine.WithHTTPAdapter(factory))
```

Here, `fasthttp` is `github.com/valyala/fasthttp`. Earlier middleware wraps later middleware. After configuration validation, factories run once per adapter. Nil functions and nil handler results are rejected. Middleware can answer native routes directly and pass other requests to `next`. Admission blocking and tracking wrap middleware; shutdown also waits for post-processing after `next` returns.

## Serving and shutdown

Ordinary applications use Spine's `RunContext`. To serve a prepared application directly, call `prepared.HTTPAdapter().Serve(listener)`. `Serve` closes the listener on every return path and runs only once per instance. Normal shutdown returns `httpcontract.ErrServerClosed`.

`StopAccepting` synchronously blocks new native entries. `Shutdown(ctx)` starts cleanup once; context expiry ends only that wait. Cleanup continues, and another call with a new context can wait again.

For TLS, set `boot.HTTPOptions.TLS` to a `*tls.Config` containing certificates. Direct serving can override the prepared TLS configuration with `Serve(listener, httpcontract.WithTLS(config))`. `httpcontract` aliases `github.com/NARUBROWN/spine/contract/http`.

## External fasthttp servers

Attach the prepared adapter's guarded native handler to an external server:

```go
adapter := prepared.HTTPAdapter().(*fasthttpPlatform.FastHTTPAdapter)
server := &fasthttp.Server{
    Handler:                      adapter.Handler(),
    StreamRequestBody:            true,
    DisablePreParseMultipartForm: true,
    MaxRequestBodySize:           4 << 10,
    ReadTimeout:                  30 * time.Second,
    IdleTimeout:                  120 * time.Second,
}
```

`time` is from the standard library. Retain the streaming and multipart settings above on external servers. The caller manages the external server's TLS, native limits, serving, and shutdown. `prepared.Close(ctx)` blocks application admission and waits for in-flight requests without closing the external server. Shut that server down separately with `server.ShutdownWithContext(ctx)`.

## Verification

Uses public modules only; no local `replace` is required.

```sh
GOWORK=off go test -race -count=1 -shuffle=on -timeout=90s ./...
GOWORK=off go vet ./...
```

Tests cover raw input and snapshot isolation, body limits and truncated streams, keepalive reuse after repeated EOF, response copying and repeated headers, concurrent request isolation, TLS, shutdown races, and external server ownership. The actual Spine composition path checks JSON DTOs, path/query/header binding, invalid JSON, body limits, and runtime OpenAPI.
