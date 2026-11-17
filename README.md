# Spine fasthttp 플랫폼

Spine 앱을 fasthttp의 HTTP/1.1 서버에서 실행합니다. HTTP 계약 `spine.http-adapter/v3`를 구현하며, 라우팅과 DTO 바인딩, 검증, OpenAPI는 Spine 코어가 처리합니다.

## Preview 설치

Go 1.26.0 이상이 필요합니다. 첫 프리뷰는 Spine `v0.7.6-preview.1`과 fasthttp `v1.79.0`을 사용합니다.

```sh
go get github.com/NARUBROWN/spine-fasthttp-platform@v0.1.0-preview.1
```

## 앱 구성

```go
import (
    "github.com/NARUBROWN/spine"
    fasthttpPlatform "github.com/NARUBROWN/spine-fasthttp-platform"
)

app := spine.New(fasthttpPlatform.Option())
```

`fasthttpPlatform.New()`는 같은 플랫폼을 연결한 앱을 반환합니다. `NewFactory()`는 `contract/http.Factory`를 반환하며, 생성 시 리스너나 실행 고루틴을 만들지 않습니다. 컨트롤러 등록과 앱 실행은 [실행 가능한 예제](examples/hello/main.go)를 참고하세요.

```sh
go run ./examples/hello
```

기본 포트는 8080입니다. `SPINE_ADDRESS=:8081 go run ./examples/hello`로 주소를 바꿀 수 있습니다.

다른 터미널에서 요청하면 `hello`를 반환합니다.

```sh
curl http://localhost:8080/hello
```

## 지원 범위

| 기능 | 동작 |
| --- | --- |
| 원시 입력 | 디코딩 전 경로와 원시 쿼리, 반복 헤더, 포트가 포함된 실제 authority를 전달합니다. |
| 요청 본문 | 스트리밍 reader를 제공하고 반복 getter의 읽기 위치를 유지합니다. |
| 본문 한도 | 기본 32 MiB이며, 음수는 한도를 해제합니다. 초과 시 원인을 보존한 HTTP 413 `RequestError`를 반환합니다. |
| 잘린 본문 | 고정 길이와 chunked 종료 오류를 정상 EOF로 바꾸지 않습니다. |
| 응답 | JSON, 문자열, bytes와 반복 응답 헤더를 지원합니다. 전달한 bytes는 쓰기 메서드 반환 후 재사용할 수 있습니다. |
| 요청 context | 풀링된 `RequestCtx`와 독립적인 표준 context를 생성하고 native 후처리 종료 시 취소합니다. |
| TLS | 준비한 인증서와 실행 시 `httpcontract.WithTLS` 교체를 지원합니다. |
| 종료 | 신규 진입을 503으로 차단하고 진행 요청과 native 후처리 완료를 기다립니다. |

본문의 native 사전 읽기는 4 KiB로 제한하고 multipart 사전 파싱은 끕니다. 전체 본문 한도는 플랫폼 reader가 적용하며, multipart 해석과 임시 파일 정리는 Spine 코어가 담당합니다. 파싱 전 네트워크 오류는 native 서버가 거부할 수도 있습니다.

이 프리뷰는 net/http handler, net/http 기반 WebSocket 업그레이드와 클라이언트 연결 해제에 따른 context 취소를 지원하지 않습니다. 해당 capability를 요구하면 준비 단계에서 거부합니다. context는 `context.Background()`에서 파생하며 native `UserValue`를 전달하지 않습니다. native 미들웨어의 패닉 복구는 필요한 경우 미들웨어에서 구성하세요. Spine 실행 중 패닉은 코어의 복구 설정을 따릅니다.

응답 writer는 최종 상태 코드 200부터 999까지 받습니다. 1xx 응답과 프로토콜 업그레이드는 지원하지 않습니다. 논리적인 응답 확정은 소켓 송신 완료를 뜻하지 않습니다.

## 서버 옵션

`boot.HTTPOptions`로 전달합니다. 지원하지 않는 옵션에 명시 값을 지정하면 `ErrUnsupportedOption`으로 거부합니다.

| 옵션 | 기본값 | 지원 |
| --- | --- | --- |
| `MaxBodyBytes` | 32 MiB | 양수 한도, 음수 제한 해제 |
| `ReadTimeout` | 30초 | 전체 요청 읽기 기한 |
| `IdleTimeout` | 120초 | 다음 keepalive 요청 대기 기한 |
| `DisableRecover` | false | Spine 코어 복구 비활성화 |
| `ReadHeaderTimeout` | 0 | 명시 값 미지원 |
| `WriteTimeout` | 0 | 명시 값 미지원 |
| `MaxHeaderBytes` | 0 | 명시 값 미지원 |

fasthttp의 `WriteTimeout`은 핸들러 반환 뒤부터 적용하므로 공통 옵션에 대응시키지 않습니다. 헤더 크기와 헤더 전용 기한에도 동일한 의미의 native 설정을 선언하지 않습니다. native 기본 읽기 버퍼는 4 KiB이며 헤더 크기도 이 버퍼의 제약을 받습니다.

## Native 미들웨어

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

이 예제의 `fasthttp`는 `github.com/valyala/fasthttp`입니다. 먼저 전달한 미들웨어를 바깥에 배치합니다. 설정 검증 후 생성 함수를 어댑터마다 한 번 호출하며 nil 함수와 nil handler 결과는 거부합니다. native 경로는 미들웨어에서 직접 응답하고 나머지는 `next`에 전달할 수 있습니다. 진입 차단과 추적은 미들웨어 바깥에서 시작하며 `next` 반환 후의 후처리도 종료 대기에 포함합니다.

## 실행과 종료

일반 앱은 Spine의 `RunContext`로 실행합니다. 준비한 앱을 직접 실행하려면 `prepared.HTTPAdapter().Serve(listener)`를 사용합니다. `Serve`는 모든 반환 경로에서 리스너를 닫고 인스턴스당 한 번만 실행합니다. 정상 종료는 `httpcontract.ErrServerClosed`를 반환합니다.

`StopAccepting`은 새 native 진입을 동기적으로 차단합니다. `Shutdown(ctx)`는 정리를 한 번 시작하고, context가 만료되면 해당 대기만 끝냅니다. 정리는 계속되므로 새 context로 다시 기다릴 수 있습니다.

TLS를 사용하려면 `boot.HTTPOptions.TLS`에 인증서가 있는 `*tls.Config`를 지정합니다. 직접 실행할 때는 `Serve(listener, httpcontract.WithTLS(config))`로 준비한 TLS 설정을 교체할 수 있습니다. `httpcontract`는 `github.com/NARUBROWN/spine/contract/http`의 import 별칭입니다.

## 외부 fasthttp 서버

준비한 어댑터의 보호된 native handler를 외부 서버에 연결할 수 있습니다.

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

`time`은 표준 라이브러리입니다. 외부 서버에서도 위 스트리밍과 multipart 설정을 유지해야 합니다. 외부 서버의 TLS와 native 한도, 실행, 종료는 호출자가 관리합니다. `prepared.Close(ctx)`는 앱의 진입 차단과 진행 요청을 기다리며 외부 서버 자체는 닫지 않습니다. 외부 서버 종료는 `server.ShutdownWithContext(ctx)`로 별도 수행합니다.

## 검증

공개 모듈만 사용하며 로컬 `replace`는 필요하지 않습니다.

```sh
GOWORK=off go test -race -count=1 -shuffle=on -timeout=90s ./...
GOWORK=off go vet ./...
```

테스트는 원시 입력과 스냅샷 격리, 본문 한도와 잘린 스트림, 반복 EOF 후 keepalive 재사용, 응답 복사와 반복 헤더, 동시 요청 격리, TLS, 종료 경쟁과 외부 서버 소유권을 확인합니다. 실제 Spine 조립 경로에서 JSON DTO와 path, query, header 바인딩, 잘못된 JSON, 본문 한도와 런타임 OpenAPI를 확인합니다.
