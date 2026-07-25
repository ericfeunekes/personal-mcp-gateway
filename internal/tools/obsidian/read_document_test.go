package obsidian

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/fsx"
)

func TestCapturePDFValidatesAndPreservesOriginalBytes(t *testing.T) {
	root := t.TempDir()
	original := syntheticPDF(t, "native-document")
	if err := os.WriteFile(filepath.Join(root, "document.pdf"), original, 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	gate := newDocumentByteGate(DocumentMaxBytes)
	snapshot, err := capturePDF(context.Background(), vault, gate, ReadDocumentInput{Path: "document.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gate.reserved(); got != int64(len(original)) {
		t.Fatalf("reserved bytes = %d", got)
	}
	if snapshot.MIMEType() != documentMIMEPDF || snapshot.Size() != int64(len(original)) {
		t.Fatalf("snapshot metadata = %q %d", snapshot.MIMEType(), snapshot.Size())
	}
	if !strings.HasPrefix(snapshot.URI(), "obsidian://read-document/") || !strings.HasSuffix(snapshot.URI(), ".pdf") || strings.Contains(snapshot.URI(), "document.pdf") {
		t.Fatalf("unsafe URI = %q", snapshot.URI())
	}
	var copied bytes.Buffer
	if err := snapshot.Stream(context.Background(), &copied); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied.Bytes(), original) {
		t.Fatal("captured bytes changed")
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if gate.reserved() != 0 {
		t.Fatalf("reserved bytes after close = %d", gate.reserved())
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestCapturePDFFailsClosedForUnsupportedMalformedAndOversized(t *testing.T) {
	root := t.TempDir()
	valid := syntheticPDF(t, "valid")
	files := map[string][]byte{
		"spoof.pdf":        []byte("%PDF-1.7\nnot a document\n%%EOF\n"),
		"truncated.pdf":    valid[:len(valid)-8],
		"polyglot.pdf":     append([]byte("PK\x03\x04"), valid...),
		"trailing.pdf":     append(append([]byte(nil), valid...), []byte("payload")...),
		"corrupt-xref.pdf": corruptPDFStartXRef(valid),
		"document.txt":     valid,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	oversized := filepath.Join(root, "oversized.pdf")
	f, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(DocumentMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	gate := newDocumentByteGate(DocumentMaxBytes)
	for _, tt := range []struct {
		name string
		want error
	}{
		{"spoof.pdf", errDocumentMalformed},
		{"truncated.pdf", errDocumentMalformed},
		{"polyglot.pdf", errDocumentMalformed},
		{"trailing.pdf", errDocumentMalformed},
		{"corrupt-xref.pdf", errDocumentMalformed},
		{"document.txt", errDocumentUnsupported},
		{"oversized.pdf", errDocumentTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := capturePDF(context.Background(), vault, gate, ReadDocumentInput{Path: tt.name})
			if got != nil || err != tt.want {
				if got != nil {
					_ = got.Close()
				}
				t.Fatalf("capture = %#v, %v; want nil, %v", got, err, tt.want)
			}
			if gate.reserved() != 0 {
				t.Fatalf("reserved bytes = %d", gate.reserved())
			}
		})
	}
}

func corruptPDFStartXRef(data []byte) []byte {
	out := append([]byte(nil), data...)
	start := bytes.LastIndex(out, []byte("startxref\n")) + len("startxref\n")
	for i := start; i < len(out) && out[i] != '\n'; i++ {
		out[i] = '0'
	}
	return out
}

func TestDocumentByteGateAllowsSmallOverlapAndRecoversFromBusy(t *testing.T) {
	gate := newDocumentByteGate(10)
	releaseA, err := gate.reserve(4)
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := gate.reserve(6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.reserve(1); err != errDocumentBusy {
		t.Fatalf("over-cap error = %v", err)
	}
	releaseA()
	releaseC, err := gate.reserve(4)
	if err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
	releaseB()
	releaseC()
	if gate.reserved() != 0 {
		t.Fatalf("reserved bytes = %d", gate.reserved())
	}
}

type busyBeforeReadValidator struct{}

func (busyBeforeReadValidator) Reserve() (func(), error) { return nil, errDocumentBusy }
func (busyBeforeReadValidator) Validate(context.Context, []byte) error {
	panic("validation must not run without admission")
}

func TestCapturePDFAcquiresValidatorCapacityBeforeOpeningSource(t *testing.T) {
	vault, err := fsx.NewVault(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gate := newDocumentByteGate(DocumentMaxBytes)
	snapshot, err := capturePDF(context.Background(), vault, gate, ReadDocumentInput{Path: "missing.pdf"}, busyBeforeReadValidator{})
	if snapshot != nil || !errors.Is(err, errDocumentBusy) {
		t.Fatalf("capture=%#v err=%v, want busy before missing-file lookup", snapshot, err)
	}
	if gate.reserved() != 0 {
		t.Fatalf("byte capacity reserved before validator admission: %d", gate.reserved())
	}
}

func TestPDFValidatorProcessSerializesAdmission(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	validator := NewPDFValidatorProcess(executable)
	release, err := validator.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Reserve(); !errors.Is(err, errDocumentBusy) {
		t.Fatalf("second admission err=%v, want busy", err)
	}
	release()
	releaseAgain, err := validator.Reserve()
	if err != nil {
		t.Fatalf("admission after release: %v", err)
	}
	releaseAgain()
}

func TestCapturePDFFailsClosedWhenSourceChangesDuringValidation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "changing.pdf")
	original := syntheticPDF(t, "before")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	gate := newDocumentByteGate(DocumentMaxBytes)
	validator := DocumentValidatorFunc(func(context.Context, []byte) error {
		return os.WriteFile(path, append(append([]byte(nil), original...), 'x'), 0600)
	})
	snapshot, err := capturePDF(context.Background(), vault, gate, ReadDocumentInput{Path: "changing.pdf"}, validator)
	if snapshot != nil || !fsx.IsCode(err, fsx.CodeSourceChanged) {
		if snapshot != nil {
			_ = snapshot.Close()
		}
		t.Fatalf("capture=%#v err=%v, want source_changed", snapshot, err)
	}
	if gate.reserved() != 0 {
		t.Fatalf("reservation leaked after source change: %d", gate.reserved())
	}
}

func TestDocumentSnapshotCancellationAndShortWrite(t *testing.T) {
	gate := newDocumentByteGate(10)
	release, err := gate.reserve(4)
	if err != nil {
		t.Fatal(err)
	}
	mapped := []byte("test")
	snapshot := &documentSnapshot{data: mapped, release: release, uri: "obsidian://read-document/test.pdf"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshot.Stream(ctx, io.Discard); err != context.Canceled {
		t.Fatalf("cancel error = %v", err)
	}
	if err := snapshot.Stream(context.Background(), zeroWriter{}); err != io.ErrShortWrite {
		t.Fatalf("short-write error = %v", err)
	}
	// Do not call Close: this test snapshot is backed by a Go slice, not mmap.
	snapshot.mu.Lock()
	snapshot.data = nil
	snapshot.release = nil
	snapshot.mu.Unlock()
	release()
}

func TestPDFValidatorWorkerUsesNoPersistentBacking(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	data := syntheticPDF(t, "isolated-validator")
	handled, code := RunPDFValidatorWorker([]string{pdfValidatorCommand, fmt.Sprint(len(data))}, bytes.NewReader(data))
	if !handled || code != 0 {
		t.Fatalf("worker handled=%v code=%d", handled, code)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("validator backing persisted: entries=%v err=%v", entries, err)
	}
	if _, code := RunPDFValidatorWorker([]string{pdfValidatorCommand, fmt.Sprint(len(data) + 1)}, bytes.NewReader(data)); code == 0 {
		t.Fatal("worker accepted truncated input")
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func syntheticPDF(t *testing.T, text string) []byte {
	t.Helper()
	content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", strings.ReplaceAll(text, ")", ""))
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}
