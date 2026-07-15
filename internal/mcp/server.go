package mcp

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
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
	ordered, names, err := validateDescriptors(descriptors)
	if err != nil {
		return nil, nil, err
	}
	server := sdk.NewServer(&sdk.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
		Icons:   serverIcons(),
	}, &sdk.ServerOptions{
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
