package mcp

import (
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
