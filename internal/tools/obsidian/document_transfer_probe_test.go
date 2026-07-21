package obsidian

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "personal-mcp-gateway/internal/mcp"
)

func TestDocumentTransferProbeFixtureAndBoundary(t *testing.T) {
	if !bytes.HasPrefix(documentTransferProbePDF, []byte("%PDF-1.4")) || !bytes.HasSuffix(documentTransferProbePDF, []byte("%%EOF\n")) {
		t.Fatal("probe fixture does not have the expected PDF boundaries")
	}
	if !bytes.Contains(documentTransferProbePDF, []byte("NDX-7Q4M-9K2P-R8VC")) {
		t.Fatal("probe fixture lacks the byte-only verification token")
	}
	near := paddedProbePDF(t, MaxDocumentTransferProbeBytes)
	if err := validateDocumentTransferProbeBytes(near); err != nil {
		t.Fatalf("near-cap fixture was rejected: %v", err)
	}
	if !bytes.HasPrefix(near, []byte("%PDF-1.4")) || !bytes.HasSuffix(near, []byte("%%EOF\n")) ||
		!bytes.Contains(near, []byte("NDX-7Q4M-9K2P-R8VC")) {
		t.Fatal("near-cap fixture lost valid PDF evidence")
	}
	over := paddedProbePDF(t, MaxDocumentTransferProbeBytes+1)
	if err := validateDocumentTransferProbeBytes(over); err == nil {
		t.Fatal("over-cap fixture was accepted")
	}
}

func paddedProbePDF(t *testing.T, size int) []byte {
	t.Helper()
	marker := []byte("%%EOF\n")
	padding := size - len(documentTransferProbePDF)
	if padding < 3 || !bytes.HasSuffix(documentTransferProbePDF, marker) {
		t.Fatalf("cannot pad probe PDF to %d bytes", size)
	}
	out := append([]byte(nil), documentTransferProbePDF[:len(documentTransferProbePDF)-len(marker)]...)
	out = append(out, '%')
	out = append(out, bytes.Repeat([]byte{'p'}, padding-2)...)
	out = append(out, '\n')
	out = append(out, marker...)
	if len(out) != size {
		t.Fatalf("padded PDF = %d bytes, want %d", len(out), size)
	}
	return out
}

func TestDocumentTransferProbeSurvivesSDKPipeTransport(t *testing.T) {
	original := documentTransferProbePDF
	near := paddedProbePDF(t, MaxDocumentTransferProbeBytes)
	documentTransferProbePDF = near
	t.Cleanup(func() { documentTransferProbePDF = original })

	tools := &Tools{}
	descriptor, err := documentTransferProbeDescriptor(tools)
	if err != nil {
		t.Fatal(err)
	}
	server, names, err := localmcp.NewServer(nil, "stdio", []localmcp.ToolDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != ToolDocumentTransferProbe {
		t.Fatalf("tool names = %#v", names)
	}

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(ctx, &sdk.IOTransport{Reader: serverReader, Writer: serverWriter})
	}()

	client := sdk.NewClient(&sdk.Implementation{Name: "document-transfer-probe-test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientReader, Writer: clientWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: ToolDocumentTransferProbe, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	embedded, ok := result.Content[0].(*sdk.EmbeddedResource)
	if !ok || embedded.Resource == nil {
		t.Fatalf("content type = %T, want embedded resource", result.Content[0])
	}
	if embedded.Resource.URI != DocumentTransferProbeURI || embedded.Resource.MIMEType != DocumentTransferProbeMIME {
		t.Fatalf("resource identity = %#v", embedded.Resource)
	}
	if !bytes.Equal(embedded.Resource.Blob, near) {
		t.Fatal("near-cap probe bytes changed across handler and SDK pipe transport")
	}

	_ = session.Close()
	cancel()
	_ = clientWriter.Close()
	_ = clientReader.Close()
	_ = serverWriter.Close()
	_ = serverReader.Close()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestDocumentTransferProbeHandlerRejectsValidPDFOneByteOverCap(t *testing.T) {
	original := documentTransferProbePDF
	documentTransferProbePDF = paddedProbePDF(t, MaxDocumentTransferProbeBytes+1)
	t.Cleanup(func() { documentTransferProbePDF = original })

	result, out, err := (&Tools{}).DocumentTransferProbe(context.Background(), nil, DocumentTransferProbeInput{})
	if err == nil || result != nil || out != (DocumentTransferProbeOutput{}) {
		t.Fatalf("over-cap handler result = %#v, %#v, %v", result, out, err)
	}
}

func TestNormalDescriptorBuildKeepsFiveToolSurface(t *testing.T) {
	original := documentTransferProbeBuild
	documentTransferProbeBuild = "disabled"
	t.Cleanup(func() { documentTransferProbeBuild = original })

	if documentTransferProbeEnabled() {
		t.Fatal("normal build unexpectedly enabled the temporary probe")
	}
}
