package mcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

const (
	ServerName    = "obsidian"
	ServerVersion = "0.1.0"
)

func NewServer(log *audit.Logger, transport string, knownTools []string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{},
	})
	if log != nil && log.Enabled() {
		server.AddReceivingMiddleware(telemetryMiddleware(log, transport, knownTools))
	}
	return server
}

func RunStdio(ctx context.Context, server *sdk.Server) error {
	return server.Run(ctx, &sdk.IOTransport{
		Reader: newLineLimitReadCloser(os.Stdin, limits.StdioMessageBytes),
		Writer: nopWriteCloser{Writer: os.Stdout},
	})
}

func StreamableHTTPHandler(server *sdk.Server) http.Handler {
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		return server
	}, &sdk.StreamableHTTPOptions{
		JSONResponse: true,
		Stateless:    true,
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
