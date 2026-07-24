package obsidian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	near := paddedProbePDF(t, 512*1024)
	if err := validateDocumentTransferProbeBytes(near); err != nil {
		t.Fatalf("near-cap fixture was rejected: %v", err)
	}
	if !bytes.HasPrefix(near, []byte("%PDF-1.4")) || !bytes.HasSuffix(near, []byte("%%EOF\n")) ||
		!bytes.Contains(near, []byte("NDX-7Q4M-9K2P-R8VC")) {
		t.Fatal("near-cap fixture lost valid PDF evidence")
	}
	if err := validateDocumentTransferProbeSize(RejectedDocumentTransferBytes); err == nil {
		t.Fatal("over-cap fixture was accepted")
	}
}

func TestGeneratedDocumentTransferProbeHasTerminalEvidence(t *testing.T) {
	const target = 32 * 1024
	data, err := generateDocumentTransferProbePDF(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != target || !bytes.HasPrefix(data, []byte("%PDF-1.7")) || !bytes.HasSuffix(data, []byte("%%EOF\n")) {
		t.Fatalf("generated fixture boundary = %d bytes", len(data))
	}
	nonceOffset := bytes.Index(data, []byte(DocumentTransferProbeNonce))
	if nonceOffset < target-2048 {
		t.Fatalf("nonce offset = %d, want terminal evidence", nonceOffset)
	}
	if bytes.Count(data, []byte(DocumentTransferProbeNonce)) != 1 {
		t.Fatal("generated fixture nonce was not unique")
	}
}

func TestExactDocumentTransferProbeIdentity(t *testing.T) {
	data, err := generateDocumentTransferProbePDF(MaxDocumentTransferProbeBytes)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != DocumentTransferProbeSHA256 {
		t.Fatalf("exact probe sha256=%s, want %s", got, DocumentTransferProbeSHA256)
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
	near := paddedProbePDF(t, 512*1024)
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

func TestDocumentTransferProbeRejectsOfficialCeiling(t *testing.T) {
	if err := validateDocumentTransferProbeSize(MaxDocumentTransferProbeBytes); err != nil {
		t.Fatalf("largest admissible size was rejected: %v", err)
	}
	if err := validateDocumentTransferProbeSize(RejectedDocumentTransferBytes); err == nil {
		t.Fatal("official ceiling was accepted")
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

func TestDocumentTransferProbeBuildModes(t *testing.T) {
	original := documentTransferProbeBuild
	t.Cleanup(func() { documentTransferProbeBuild = original })

	documentTransferProbeBuild = "enabled"
	if !documentTransferProbeEnabled() || documentTransferCapacityEnabled() || DocumentTransferProbeExpectedSHA256() != documentTransferBaseSHA256 {
		t.Fatal("ordinary probe mode changed")
	}
	documentTransferProbeBuild = "capacity"
	if !documentTransferProbeEnabled() || !documentTransferCapacityEnabled() || DocumentTransferProbeExpectedSHA256() != DocumentTransferProbeSHA256 {
		t.Fatal("capacity probe mode changed")
	}
}

func TestDocumentTransferProbeIdentityIsSelectedByReturnedSize(t *testing.T) {
	if digest, ok := DocumentTransferProbeSHA256ForSize(len(documentTransferProbePDF)); !ok || digest != documentTransferBaseSHA256 {
		t.Fatal("small probe identity changed")
	}
	if digest, ok := DocumentTransferProbeSHA256ForSize(MaxDocumentTransferProbeBytes); !ok || digest != DocumentTransferProbeSHA256 {
		t.Fatal("capacity probe identity changed")
	}
	if _, ok := DocumentTransferProbeSHA256ForSize(MaxDocumentTransferProbeBytes - 1); ok {
		t.Fatal("unfrozen probe size was accepted")
	}
}
