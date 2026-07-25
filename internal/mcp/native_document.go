package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	nativeDocumentMarkerKey = "io.github.ericfeunekes.personal-mcp-gateway.native-document.v1"
	nativeDocumentTakeTTL   = 5 * time.Second
)

var (
	ErrNativeDocumentUnavailable = errors.New("native document payload is unavailable")
	ErrNativeDocumentClosed      = errors.New("native document bridge is closed")
)

// NativeDocumentPayload is an opaque, one-shot native document response. Its
// metadata must already be safe to return to a client.
type NativeDocumentPayload interface {
	URI() string
	MIMEType() string
	Size() int64
	Metadata() map[string]any
	Stream(context.Context, io.Writer) error
	Close() error
}

// NativeDocumentBridge owns short-lived payloads between a tool handler and
// the transport's raw response writer. It never stores source paths or bytes.
type NativeDocumentBridge struct {
	mu      sync.Mutex
	entries map[string]*nativeDocumentEntry
	closed  bool
	ttl     time.Duration
}

type nativeDocumentEntry struct {
	payload NativeDocumentPayload
	timer   *time.Timer
}

func NewNativeDocumentBridge() *NativeDocumentBridge {
	return &NativeDocumentBridge{entries: make(map[string]*nativeDocumentEntry), ttl: nativeDocumentTakeTTL}
}

// Register stores payload until the matching marker is consumed. The returned
// metadata is private transport state and must be attached only to the SDK
// CallToolResult that represents this payload.
func (b *NativeDocumentBridge) Register(payload NativeDocumentPayload) (sdk.Meta, error) {
	if b == nil || payload == nil || payload.URI() == "" || payload.MIMEType() == "" || payload.Size() < 0 {
		return nil, fmt.Errorf("register native document: invalid payload")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("register native document: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrNativeDocumentClosed
	}
	entry := &nativeDocumentEntry{payload: payload}
	entry.timer = time.AfterFunc(b.ttl, func() { b.expire(token, entry) })
	b.entries[token] = entry
	return sdk.Meta{nativeDocumentMarkerKey: token}, nil
}

func (b *NativeDocumentBridge) expire(token string, expected *nativeDocumentEntry) {
	b.mu.Lock()
	entry, ok := b.entries[token]
	if !ok || entry != expected {
		b.mu.Unlock()
		return
	}
	delete(b.entries, token)
	b.mu.Unlock()
	_ = entry.payload.Close()
}

func (b *NativeDocumentBridge) take(token string) (NativeDocumentPayload, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrNativeDocumentUnavailable
	}
	entry, ok := b.entries[token]
	if !ok {
		return nil, ErrNativeDocumentUnavailable
	}
	delete(b.entries, token)
	entry.timer.Stop()
	return entry.payload, nil
}

// Close releases every payload that was registered but not yet taken.
func (b *NativeDocumentBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	entries := b.entries
	b.entries = nil
	b.mu.Unlock()
	var err error
	for _, entry := range entries {
		entry.timer.Stop()
		err = errors.Join(err, entry.payload.Close())
	}
	return err
}

type markerResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  struct {
		Meta map[string]any `json:"_meta"`
	} `json:"result"`
}

func nativeDocumentToken(frame []byte) (string, bool) {
	var response markerResponse
	if len(frame) == 0 || frame[0] == '[' || json.Unmarshal(frame, &response) != nil || response.JSONRPC != "2.0" || len(response.ID) == 0 {
		return "", false
	}
	token, ok := response.Result.Meta[nativeDocumentMarkerKey].(string)
	return token, ok && token != ""
}

func writeNativeDocument(ctx context.Context, w io.Writer, frame []byte, bridge *NativeDocumentBridge) (bool, error) {
	if bridge == nil {
		return false, nil
	}
	token, marked := nativeDocumentToken(frame)
	if !marked {
		return false, nil
	}
	payload, err := bridge.take(token)
	if err != nil {
		return true, err
	}
	return true, writeNativeDocumentPayload(ctx, w, frame, payload)
}

func writeNativeDocumentPayload(ctx context.Context, w io.Writer, frame []byte, payload NativeDocumentPayload) error {
	defer payload.Close()
	meta := payload.Metadata()
	if meta == nil {
		meta = map[string]any{}
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal native document metadata: %w", err)
	}
	var response markerResponse
	if err := json.Unmarshal(frame, &response); err != nil {
		return err
	}
	prefix := []byte(`{"jsonrpc":"2.0","id":` + string(response.ID) + `,"result":{"content":[{"type":"resource","resource":{"uri":`)
	uri, _ := json.Marshal(payload.URI())
	mime, _ := json.Marshal(payload.MIMEType())
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	if _, err := w.Write(uri); err != nil {
		return err
	}
	if _, err := w.Write([]byte(`,"mimeType":`)); err != nil {
		return err
	}
	if _, err := w.Write(mime); err != nil {
		return err
	}
	if _, err := w.Write([]byte(`,"_meta":`)); err != nil {
		return err
	}
	if _, err := w.Write(metaBytes); err != nil {
		return err
	}
	if _, err := w.Write([]byte(`,"blob":"`)); err != nil {
		return err
	}
	encoder := base64.NewEncoder(base64.StdEncoding, w)
	if err := payload.Stream(ctx, encoder); err != nil {
		_ = encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if _, err := w.Write([]byte(`"}}],"structuredContent":`)); err != nil {
		return err
	}
	if _, err := w.Write(metaBytes); err != nil {
		return err
	}
	_, err = w.Write([]byte(`}}`))
	return err
}
