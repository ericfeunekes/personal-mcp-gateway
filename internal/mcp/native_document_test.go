package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type testNativePayload struct {
	uri, mime string
	data      []byte
	closed    atomic.Int32
}

func (p *testNativePayload) URI() string      { return p.uri }
func (p *testNativePayload) MIMEType() string { return p.mime }
func (p *testNativePayload) Size() int64      { return int64(len(p.data)) }
func (p *testNativePayload) Metadata() map[string]any {
	return map[string]any{"byte_count": len(p.data)}
}
func (p *testNativePayload) Stream(_ context.Context, w io.Writer) error {
	_, err := w.Write(p.data)
	return err
}
func (p *testNativePayload) Close() error { p.closed.Add(1); return nil }

func markerFrame(t *testing.T, meta map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "result": map[string]any{"_meta": meta}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNativeDocumentWireReplacementDoesNotLeakMarker(t *testing.T) {
	bridge := NewNativeDocumentBridge()
	payload := &testNativePayload{uri: "obsidian://read-document/random.pdf", mime: "application/pdf", data: []byte("%PDF-test")}
	meta, err := bridge.Register(payload)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	marked, err := writeNativeDocument(context.Background(), &out, markerFrame(t, meta), bridge)
	if err != nil || !marked {
		t.Fatalf("write marked=%v err=%v", marked, err)
	}
	if strings.Contains(out.String(), nativeDocumentMarkerKey) {
		t.Fatalf("marker leaked: %s", out.String())
	}
	var got struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Type     string `json:"type"`
				Resource struct {
					URI  string `json:"uri"`
					MIME string `json:"mimeType"`
					Blob string `json:"blob"`
				} `json:"resource"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Result.Content) != 1 || got.Result.Content[0].Type != "resource" || got.Result.Content[0].Resource.URI != payload.uri || got.Result.Content[0].Resource.MIME != payload.mime || got.Result.Content[0].Resource.Blob != "JVBERi10ZXN0" {
		t.Fatalf("unexpected native response: %s", out.String())
	}
	if got.Result.StructuredContent["byte_count"] != float64(len(payload.data)) {
		t.Fatalf("structured metadata changed: %#v", got.Result.StructuredContent)
	}
	if payload.closed.Load() != 1 {
		t.Fatalf("close calls=%d, want 1", payload.closed.Load())
	}
}

func TestNativeDocumentWireReplacementPreservesSDKEnvelope(t *testing.T) {
	bridge := NewNativeDocumentBridge()
	payload := &testNativePayload{uri: "obsidian://read-document/envelope.pdf", mime: "application/pdf", data: []byte("pdf")}
	meta, err := bridge.Register(payload)
	if err != nil {
		t.Fatal(err)
	}
	marker := meta[nativeDocumentMarkerKey]
	frame := []byte(`{"jsonrpc":"2.0","id":7,"result":{"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"obsidian","version":"0.1.0"},"` + nativeDocumentMarkerKey + `":"` + marker.(string) + `"},"content":[]}}`)
	var out bytes.Buffer
	if err := writeNativeDocumentPayload(context.Background(), &out, frame, payload); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Result struct {
			ResultType string         `json:"resultType"`
			Meta       map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Result.ResultType != "complete" || got.Result.Meta["io.modelcontextprotocol/serverInfo"] == nil {
		t.Fatalf("SDK envelope was not preserved: %s", out.Bytes())
	}
	if _, found := got.Result.Meta[nativeDocumentMarkerKey]; found {
		t.Fatalf("private marker leaked: %s", out.Bytes())
	}
}

func TestNativeDocumentExpiryAndShutdownCloseUntakenPayloads(t *testing.T) {
	bridge := NewNativeDocumentBridge()
	bridge.ttl = time.Millisecond
	expired := &testNativePayload{uri: "obsidian://read-document/a.pdf", mime: "application/pdf"}
	meta, err := bridge.Register(expired)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	var out bytes.Buffer
	if marked, err := writeNativeDocument(context.Background(), &out, markerFrame(t, meta), bridge); !marked || err == nil || out.Len() != 0 {
		t.Fatalf("expired marker marked=%v err=%v bytes=%d", marked, err, out.Len())
	}
	if expired.closed.Load() != 1 {
		t.Fatalf("expiry did not close payload")
	}
	pending := &testNativePayload{uri: "obsidian://read-document/b.pdf", mime: "application/pdf"}
	if _, err := bridge.Register(pending); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if pending.closed.Load() != 1 {
		t.Fatalf("shutdown did not close payload")
	}
}

func TestNativeDocumentHTTPPreservesOrdinaryResponseAndRejectsNativeBatch(t *testing.T) {
	bridge := NewNativeDocumentBridge()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Original", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	})
	handler := nativeDocumentHTTPHandler(next, bridge)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusCreated || response.Header().Get("X-Original") != "yes" || response.Body.String() != `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` {
		t.Fatalf("ordinary response changed: code=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	called := false
	reject := nativeDocumentHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		called = true
	}), bridge)
	batch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_document"}}]`
	response = httptest.NewRecorder()
	reject.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(batch)))
	if response.Code != http.StatusBadRequest || called {
		t.Fatalf("native batch code=%d called=%v", response.Code, called)
	}
	response = httptest.NewRecorder()
	reject.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(" \n\t"+batch)))
	if response.Code != http.StatusBadRequest || called {
		t.Fatalf("whitespace native batch code=%d called=%v", response.Code, called)
	}
}

func TestNativeDocumentHTTPPreservesLargeOrdinaryResponse(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"value":"` + strings.Repeat("x", 70<<10) + `"}}`)
	handler := nativeDocumentHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }), NewNativeDocumentBridge())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), body) {
		t.Fatalf("large ordinary response changed: status=%d bytes=%d", response.Code, response.Body.Len())
	}
}

func TestNativeDocumentHTTPBoundsRequestBeforeDispatch(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, &sdk.ServerOptions{})
	handler := StreamableHTTPHandlerWithNativeDocuments(server, NewNativeDocumentBridge(), "")
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", int(nativeDocumentMarkerBytes)+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status=%d", response.Code)
	}
}

func TestNativeDocumentHTTPPreservesNativeHeadersAndStatus(t *testing.T) {
	bridge := NewNativeDocumentBridge()
	payload := &testNativePayload{uri: "obsidian://read-document/http.pdf", mime: "application/pdf", data: []byte("pdf")}
	meta, err := bridge.Register(payload)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Transport", "sdk")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(markerFrame(t, meta))
	})
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	nativeDocumentHTTPHandler(next, bridge).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusAccepted || response.Header().Get("X-Transport") != "sdk" {
		t.Fatalf("native response status=%d headers=%v", response.Code, response.Header())
	}
	if strings.Contains(response.Body.String(), nativeDocumentMarkerKey) || !strings.Contains(response.Body.String(), "obsidian://read-document/http.pdf") {
		t.Fatalf("native response was not replaced: %s", response.Body.String())
	}
}

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (*deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestNativeDocumentHTTPFailsClosedWithoutResponseDeadline(t *testing.T) {
	bridge := NewNativeDocumentBridge()
	payload := &testNativePayload{uri: "obsidian://read-document/deadline.pdf", mime: "application/pdf", data: []byte("pdf")}
	meta, err := bridge.Register(payload)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(markerFrame(t, meta)) })
	response := httptest.NewRecorder()
	nativeDocumentHTTPHandler(next, bridge).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), payload.uri) {
		t.Fatalf("deadline-less response did not fail closed: status=%d body=%q", response.Code, response.Body.String())
	}
	if payload.closed.Load() != 1 {
		t.Fatalf("deadline-less response close calls=%d, want 1", payload.closed.Load())
	}
}

type failAfterWriter struct {
	remaining int
}

func (w *failAfterWriter) Write(data []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	if len(data) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, io.ErrClosedPipe
	}
	w.remaining -= len(data)
	return len(data), nil
}

func TestNativeDocumentMidstreamFailureClosesPayload(t *testing.T) {
	payload := &testNativePayload{uri: "obsidian://read-document/stopped.pdf", mime: "application/pdf", data: bytes.Repeat([]byte("x"), 4096)}
	err := writeNativeDocumentPayload(context.Background(), &failAfterWriter{remaining: 128}, markerFrame(t, nil), payload)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("midstream failure err=%v, want closed pipe", err)
	}
	if payload.closed.Load() != 1 {
		t.Fatalf("midstream failure close calls=%d, want 1", payload.closed.Load())
	}
}

func TestNativeDocumentLineReaderBoundsNoNewlineInput(t *testing.T) {
	input := io.NopCloser(strings.NewReader(strings.Repeat("x", 4097)))
	reader := newNativeDocumentLineReader(input, 4096)
	buffer := make([]byte, 8)
	if n, err := reader.Read(buffer); n != 0 || !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("oversized no-newline read n=%d err=%v", n, err)
	}
}

func TestNativeDocumentWriterPreservesOrdinaryFrames(t *testing.T) {
	var out bytes.Buffer
	w := nativeDocumentWriter{ctx: context.Background(), writer: &out, bridge: NewNativeDocumentBridge()}
	input := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n")
	if n, err := w.Write(input); err != nil || n != len(input) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if !bytes.Equal(out.Bytes(), input) {
		t.Fatalf("ordinary frame changed: %q", out.Bytes())
	}
}
