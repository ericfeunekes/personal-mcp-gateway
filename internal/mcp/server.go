package mcp

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

const (
	ServerName    = "obsidian"
	ServerVersion = "0.1.0"
)

// iconSVG is Obsidian's unmodified official gradient mark, downloaded from
// https://obsidian.md/images/obsidian-logo-gradient.svg.
//
//go:embed obsidian-mcp-icon.svg
var iconSVG []byte

func serverIcons() []sdk.Icon {
	return []sdk.Icon{{
		Source:   "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(iconSVG),
		MIMEType: "image/svg+xml",
		Sizes:    []string{"any"},
		Theme:    sdk.IconThemeDark,
	}}
}

func NewServer(log *audit.Logger, transport string, descriptors []ToolDescriptor) (*sdk.Server, []string, error) {
	return NewNamedServer(sdk.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
		Icons:   serverIcons(),
	}, log, transport, descriptors)
}

// NewNamedServer constructs a separately named MCP server using the shared
// transport, registration, and telemetry behavior. Callers own the public
// server identity, including whether an integration publishes an icon.
func NewNamedServer(identity sdk.Implementation, log *audit.Logger, transport string, descriptors []ToolDescriptor) (*sdk.Server, []string, error) {
	ordered, names, err := validateDescriptors(descriptors)
	if err != nil {
		return nil, nil, err
	}
	server := sdk.NewServer(&identity, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{},
	})
	if log != nil && log.Enabled() {
		server.AddReceivingMiddleware(telemetryMiddleware(log, transport, ordered))
	}
	for _, descriptor := range ordered {
		if err := registerDescriptor(server, descriptor); err != nil {
			return nil, nil, err
		}
	}
	return server, names, nil
}

func registerDescriptor(server *sdk.Server, descriptor ToolDescriptor) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("register tool %q: %v", descriptor.Name(), recovered)
		}
	}()
	descriptor.register(server)
	return nil
}

func RunStdio(ctx context.Context, server *sdk.Server) error {
	return runStdio(ctx, server, nil)
}

// RunStdioWithNativeDocuments attaches the process-owned one-shot bridge to
// stdio without changing ordinary SDK frames.
func RunStdioWithNativeDocuments(ctx context.Context, server *sdk.Server, bridge *NativeDocumentBridge) error {
	return runStdio(ctx, server, bridge)
}

func runStdio(ctx context.Context, server *sdk.Server, bridge *NativeDocumentBridge) error {
	writer := io.WriteCloser(nopWriteCloser{Writer: os.Stdout})
	reader := io.ReadCloser(newLineLimitReadCloser(os.Stdin, limits.StdioMessageBytes))
	if bridge != nil {
		bounded, err := newPollWriteCloser(os.Stdout, nativeDocumentResponseDeadline)
		if err != nil {
			return fmt.Errorf("native document stdio writer: %w", err)
		}
		writer = &nativeDocumentWriter{ctx: ctx, writer: bounded, bridge: bridge, closer: bounded}
		reader = newNativeDocumentLineReader(os.Stdin, limits.StdioMessageBytes)
	}
	return server.Run(ctx, &sdk.IOTransport{
		Reader: reader,
		Writer: writer,
	})
}

// StreamableHTTPHandlerWithNativeDocuments serves MCP over Streamable HTTP.
// allowedHost is the one exact non-loopback Host name accepted on the
// loopback listener, such as the tailnet name `tailscale serve` forwards; an
// empty value accepts loopback Host names only.
func StreamableHTTPHandlerWithNativeDocuments(server *sdk.Server, bridge *NativeDocumentBridge, allowedHost string) http.Handler {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		return server
	}, &sdk.StreamableHTTPOptions{
		JSONResponse:                 true,
		Stateless:                    true,
		MaxRequestBodyBytes:          limits.HTTPRequestBodyBytes,
		PropagateRequestCancellation: true,
		// requireAllowedHost replaces the SDK's loopback-only Host check so the
		// configured tailnet name can pass without accepting any other name.
		DisableLocalhostProtection: true,
	})
	return requireAllowedHost(allowedHost, http.NewCrossOriginProtection().Handler(
		limitHTTPRequestBody(nativeDocumentHTTPHandler(handler, bridge))))
}

// requireAllowedHost is DNS-rebinding protection for the loopback listener. A
// request that arrived on a loopback socket must name a loopback Host or the
// single configured allowedHost; any other Host is rejected before dispatch.
func requireAllowedHost(allowedHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if localAddr, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && localAddr != nil &&
			isLoopbackHost(localAddr.String()) && !isLoopbackHost(req.Host) && !hostNameIs(req.Host, allowedHost) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, req)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostOnly(hostport)
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func hostNameIs(hostport, name string) bool {
	return name != "" && strings.EqualFold(hostOnly(hostport), name)
}

func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// limitHTTPRequestBody preserves the gateway's body-limit precedence over
// SDK media-type validation. Declared oversized bodies are rejected without a
// read. Chunked bodies have no declared length, so determining that they
// exceed the limit necessarily consumes at most limit+1 source bytes before
// handing a bounded request on to the SDK.
func limitHTTPRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && req.Body != nil {
			if req.ContentLength > limits.HTTPRequestBodyBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if req.ContentLength < 0 {
				body, err := io.ReadAll(io.LimitReader(req.Body, limits.HTTPRequestBodyBytes+1))
				_ = req.Body.Close()
				if err != nil {
					http.Error(w, "invalid request body", http.StatusBadRequest)
					return
				}
				if int64(len(body)) > limits.HTTPRequestBodyBytes {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
			}
		}
		next.ServeHTTP(w, req)
	})
}

var errMessageTooLarge = errors.New("mcp message too large")

type lineLimitReadCloser struct {
	reader  *bufio.Reader
	closer  io.Closer
	max     int64
	current int64
	tooBig  bool
}

func newLineLimitReadCloser(r io.ReadCloser, max int64) *lineLimitReadCloser {
	return &lineLimitReadCloser{
		reader: bufio.NewReader(r),
		closer: r,
		max:    max,
	}
}

func (r *lineLimitReadCloser) Read(p []byte) (int, error) {
	if r.tooBig {
		return 0, errMessageTooLarge
	}
	n, err := r.reader.Read(p)
	for i, b := range p[:n] {
		r.current++
		if r.current > r.max {
			r.tooBig = true
			if i == 0 {
				return 0, errMessageTooLarge
			}
			return i, nil
		}
		if b == '\n' {
			r.current = 0
		}
	}
	return n, err
}

func (r *lineLimitReadCloser) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

type nopWriteCloser struct {
	io.Writer
}

func (n nopWriteCloser) Close() error {
	return nil
}
