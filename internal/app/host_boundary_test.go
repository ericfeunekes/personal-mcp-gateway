package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
)

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"host-boundary","version":"0"}}}`

// TestHTTPHostBoundaryOnLoopbackListener proves the /mcp Host and
// cross-origin rules through a real loopback socket, the only place the
// gateway listens: loopback names and the one configured tailnet name reach
// dispatch; every other Host and every cross-site browser request is refused.
func TestHTTPHostBoundaryOnLoopbackListener(t *testing.T) {
	const tailnet = "mac.example.ts.net"
	for _, test := range []struct {
		name        string
		allowedHost string
		host        string
		headers     map[string]string
		want        int
	}{
		{name: "loopback ip", allowedHost: tailnet, want: http.StatusOK},
		{name: "localhost name", allowedHost: tailnet, host: "localhost:8765", want: http.StatusOK},
		{name: "configured tailnet name", allowedHost: tailnet, host: tailnet, want: http.StatusOK},
		{name: "configured tailnet name any case", allowedHost: tailnet, host: "Mac.Example.TS.net", want: http.StatusOK},
		{name: "other tailnet name", allowedHost: tailnet, host: "other.example.ts.net", want: http.StatusForbidden},
		{name: "rebinding name", allowedHost: tailnet, host: "attacker.example.com", want: http.StatusForbidden},
		{name: "suffix of configured name", allowedHost: tailnet, host: "evil" + tailnet, want: http.StatusForbidden},
		{name: "tailnet name without configuration", host: tailnet, want: http.StatusForbidden},
		{name: "cross-site browser fetch", allowedHost: tailnet, host: tailnet, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, want: http.StatusForbidden},
		{name: "foreign browser origin", allowedHost: tailnet, host: tailnet, headers: map[string]string{"Origin": "https://attacker.example.com"}, want: http.StatusForbidden},
		{name: "same-origin browser fetch", allowedHost: tailnet, host: tailnet, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := config.Validate(config.Config{
				Mode:         config.ModeHTTP,
				ObsidianRoot: t.TempDir(),
				AllowedHost:  test.allowedHost,
				Telemetry:    config.TelemetryOff,
			})
			if err != nil {
				t.Fatal(err)
			}
			application, err := New(cfg, audit.Disabled())
			if err != nil {
				t.Fatal(err)
			}
			defer application.Close()
			server := httptest.NewServer(application.HTTPHandler())
			defer server.Close()

			req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(initializeBody))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if test.host != "" {
				req.Host = test.host
			}
			for name, value := range test.headers {
				req.Header.Set(name, value)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != test.want {
				t.Fatalf("status = %d, want %d; body %q", resp.StatusCode, test.want, body)
			}
			if test.want == http.StatusOK && !strings.Contains(string(body), `"serverInfo"`) {
				t.Fatalf("initialize did not reach MCP dispatch: %q", body)
			}
		})
	}
}
