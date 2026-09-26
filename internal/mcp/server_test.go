package mcp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const sdk170Protocol = "2026-07-28"

type sdk170Output struct {
	Body string `json:"body"`
}

func sdk170Request(method, params string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
}

func sdk170Meta() string {
	return `{"io.modelcontextprotocol/protocolVersion":"` + sdk170Protocol + `","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
}

func sdk170Server(t *testing.T, handler sdk.ToolHandlerFor[struct{}, sdk170Output]) *sdk.Server {
	t.Helper()
	descriptor, err := NewToolDescriptor(sdk.Tool{Name: "probe"}, handler, noArgumentSummary, noResultSummary)
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := NewNamedServer(sdk.Implementation{Name: "probe", Version: "1"}, nil, "stdio", []ToolDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestServerIconsAdvertiseEmbeddedSVG(t *testing.T) {
	icons := serverIcons()
	if len(icons) != 1 {
		t.Fatalf("icon count = %d, want 1", len(icons))
	}
	icon := icons[0]
	const prefix = "data:image/svg+xml;base64,"
	if !strings.HasPrefix(icon.Source, prefix) {
		t.Fatalf("icon source = %q, want data SVG URI", icon.Source)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(icon.Source, prefix))
	if err != nil {
		t.Fatalf("decode icon data URI: %v", err)
	}
	if !strings.Contains(string(decoded), "<svg") {
		t.Fatal("decoded icon is not SVG")
	}
	if icon.MIMEType != "image/svg+xml" || len(icon.Sizes) != 1 || icon.Sizes[0] != "any" || icon.Theme != sdk.IconThemeDark {
		t.Fatalf("icon metadata = %#v, want scalable SVG metadata", icon)
	}
}

func TestLineLimitReadCloserRejectsOversizedMessage(t *testing.T) {
	reader := newLineLimitReadCloser(io.NopCloser(strings.NewReader("abcdx\n")), 4)
	buf := make([]byte, 2)
	var got strings.Builder

	for {
		n, err := reader.Read(buf)
		got.Write(buf[:n])
		if err == nil {
			continue
		}
		if !errors.Is(err, errMessageTooLarge) {
			t.Fatalf("Read() error = %v, want errMessageTooLarge", err)
		}
		if got.String() != "abcd" {
			t.Fatalf("bytes before limit error = %q, want %q", got.String(), "abcd")
		}
		return
	}
}

func TestLineLimitReadCloserResetsAfterNewline(t *testing.T) {
	reader := newLineLimitReadCloser(io.NopCloser(strings.NewReader("abcd\nabcd\n")), 5)
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "abcd\nabcd\n" {
		t.Fatalf("ReadAll() = %q", data)
	}
}

func TestSDK170HTTPProtocolsAndBodyLimit(t *testing.T) {
	server := sdk170Server(t, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, sdk170Output, error) {
		return nil, sdk170Output{Body: "ready"}, nil
	})
	handler := StreamableHTTPHandlerWithNativeDocuments(server, nil, "")

	legacy := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(sdk170Request("initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`)))
	legacy.Header.Set("Content-Type", "application/json")
	legacy.Header.Set("Accept", "application/json, text/event-stream")
	legacyResponse := httptest.NewRecorder()
	handler.ServeHTTP(legacyResponse, legacy)
	if legacyResponse.Code != http.StatusOK {
		t.Fatalf("legacy initialize status=%d body=%s", legacyResponse.Code, legacyResponse.Body.String())
	}
	assertSDK170ResultType(t, legacyResponse.Body.Bytes(), false)

	newRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(sdk170Request("tools/call", `{"name":"probe","arguments":{},"_meta":`+sdk170Meta()+`}`)))
	newRequest.Header.Set("Content-Type", "application/json")
	newRequest.Header.Set("Accept", "application/json, text/event-stream")
	newRequest.Header.Set("MCP-Protocol-Version", sdk170Protocol)
	newRequest.Header.Set("Mcp-Method", "tools/call")
	newRequest.Header.Set("Mcp-Name", "probe")
	newResponse := httptest.NewRecorder()
	handler.ServeHTTP(newResponse, newRequest)
	if newResponse.Code != http.StatusOK {
		t.Fatalf("new protocol status=%d body=%s", newResponse.Code, newResponse.Body.String())
	}
	assertSDK170ResultType(t, newResponse.Body.Bytes(), true)

	oversized := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 1<<20+1)))
	oversized.Header.Set("Content-Type", "application/json")
	oversized.Header.Set("Accept", "application/json, text/event-stream")
	tooLarge := httptest.NewRecorder()
	handler.ServeHTTP(tooLarge, oversized)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", tooLarge.Code, tooLarge.Body.String())
	}

	chunked := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("y", 1<<20+1)))
	chunked.ContentLength = -1
	chunkedTooLarge := httptest.NewRecorder()
	handler.ServeHTTP(chunkedTooLarge, chunked)
	if chunkedTooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized status=%d body=%s", chunkedTooLarge.Code, chunkedTooLarge.Body.String())
	}
}

func TestSDK170StdioProtocols(t *testing.T) {
	for _, request := range []struct {
		name        string
		request     string
		newProtocol bool
	}{
		{"legacy", sdk170Request("initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`), false},
		{"new", sdk170Request("server/discover", `{"_meta":`+sdk170Meta()+`}`), true},
	} {
		t.Run(request.name, func(t *testing.T) {
			serverReader, clientWriter := io.Pipe()
			clientReader, serverWriter := io.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			server := sdk170Server(t, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, sdk170Output, error) {
				return nil, sdk170Output{}, nil
			})
			go func() { done <- server.Run(ctx, &sdk.IOTransport{Reader: serverReader, Writer: serverWriter}) }()
			if _, err := fmt.Fprintln(clientWriter, request.request); err != nil {
				t.Fatal(err)
			}
			frame, err := bufio.NewReader(clientReader).ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			assertSDK170ResultType(t, frame, request.newProtocol)
			_ = clientWriter.Close()
			_ = clientReader.Close()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("stdio server did not stop")
			}
		})
	}
}

func TestSDK170NewProtocolCancellationReachesTool(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := sdk170Server(t, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, sdk170Output, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, sdk170Output{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(sdk170Request("tools/call", `{"name":"probe","arguments":{},"_meta":`+sdk170Meta()+`}`))).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", sdk170Protocol)
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "probe")
	go StreamableHTTPHandlerWithNativeDocuments(server, nil, "").ServeHTTP(httptest.NewRecorder(), request)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool handler did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("request cancellation did not reach tool context")
	}
}

func assertSDK170ResultType(t *testing.T, frame []byte, newProtocol bool) {
	t.Helper()
	var response struct {
		Result struct {
			ResultType string `json:"resultType"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode response %s: %v", frame, err)
	}
	if newProtocol && response.Result.ResultType != "complete" {
		t.Fatalf("new-protocol result type = %q, frame=%s", response.Result.ResultType, frame)
	}
	if !newProtocol && response.Result.ResultType != "" {
		t.Fatalf("legacy result type = %q, frame=%s", response.Result.ResultType, frame)
	}
}
