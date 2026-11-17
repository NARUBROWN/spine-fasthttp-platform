package platform

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	httpcontract "github.com/NARUBROWN/spine/contract/http"
)

func TestTLSPreparedAndServeOverride(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "override"}[override], func(t *testing.T) {
			config := httpcontract.Config{Execute: func(c httpcontract.HTTPContext) error { return c.Writer().WriteString(200, "secure") }}
			tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
			var options []httpcontract.ServeOption
			if override {
				options = []httpcontract.ServeOption{httpcontract.WithTLS(tlsConfig)}
			} else {
				config.Serve.TLS = tlsConfig
			}
			a := configured(t, config)
			address := managed(t, a, options...)
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			response, err := client.Get("https://" + address + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != 200 || string(body) != "secure" || response.TLS == nil {
				t.Fatalf("TLS request: %d %q %v", response.StatusCode, body, err)
			}
		})
	}
}
