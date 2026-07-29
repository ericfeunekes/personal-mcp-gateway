package obsidian

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/sys/unix"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
	localmcp "personal-mcp-gateway/internal/mcp"
)

const (
	ToolReadDocument        = "read_document"
	DocumentMaxBytes        = 7_000_000
	documentChunkSize       = 256 << 10
	documentMIMEPDF         = "application/pdf"
	ReadDocumentDescription = "Return one supported vault document as its original native bytes for model-native reading. The PDF activation supports one explicit vault-relative .pdf path up to 7,000,000 bytes; other document formats remain unsupported."
)

// Set only on a locally proven activation candidate. Ordinary builds retain
// the default ten-tool non-document surface.
var documentReadingBuild = "disabled"

func DocumentReadingCandidateEnabled() bool { return documentReadingBuild == "pdf_candidate" }

var (
	errDocumentBusy        = errors.New("native document capacity is busy")
	errDocumentEmpty       = errors.New("native document is empty")
	errDocumentTooLarge    = errors.New("native document is too large")
	errDocumentUnsupported = errors.New("native document type is unsupported")
	errDocumentMalformed   = errors.New("native document is malformed")

	disablePDFCPUConfig sync.Once
)

type documentByteGate struct {
	mu       sync.Mutex
	used     int64
	capacity int64
}

func newDocumentByteGate(capacity int64) *documentByteGate {
	return &documentByteGate{capacity: capacity}
}

func (g *documentByteGate) reserve(size int64) (func(), error) {
	if g == nil || size <= 0 || size > g.capacity {
		return nil, errDocumentTooLarge
	}
	g.mu.Lock()
	if size > g.capacity-g.used {
		g.mu.Unlock()
		return nil, errDocumentBusy
	}
	g.used += size
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.used -= size
			g.mu.Unlock()
		})
	}, nil
}

func (g *documentByteGate) reserved() int64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

type documentSnapshot struct {
	mu      sync.Mutex
	data    []byte
	release func()
	uri     string
}

func (s *documentSnapshot) URI() string { return s.uri }

func (*documentSnapshot) MIMEType() string { return documentMIMEPDF }

func (s *documentSnapshot) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.data))
}

func (s *documentSnapshot) Metadata() map[string]any {
	return map[string]any{
		"ok":        true,
		"format":    "pdf",
		"mime_type": documentMIMEPDF,
		"raw_bytes": s.Size(),
	}
}

func (s *documentSnapshot) Stream(ctx context.Context, w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		return errors.New("native document snapshot is closed")
	}
	for offset := 0; offset < len(s.data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+documentChunkSize, len(s.data))
		n, err := w.Write(s.data[offset:end])
		offset += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (s *documentSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	data := s.data
	s.data = nil
	release := s.release
	s.release = nil
	s.mu.Unlock()

	var err error
	if data != nil {
		err = unix.Munmap(data)
	}
	if release != nil {
		release()
	}
	return err
}

type ReadDocumentInput struct {
	Path string `json:"path" jsonschema:"vault-relative path to one supported document"`
	Base string `json:"base,omitempty" jsonschema:"optional vault-relative base path"`
}

type ReadDocumentOutput struct {
	OK       bool       `json:"ok"`
	Format   string     `json:"format,omitempty"`
	MIMEType string     `json:"mime_type,omitempty"`
	RawBytes int64      `json:"raw_bytes,omitempty"`
	Error    *ToolError `json:"error,omitempty"`
}

func readDocumentDescriptor(vault *fsx.Vault, bridge *localmcp.NativeDocumentBridge, gate *documentByteGate, validator DocumentValidator) (localmcp.ToolDescriptor, error) {
	return localmcp.NewToolDescriptor(sdk.Tool{
		Name:        ToolReadDocument,
		Description: ReadDocumentDescription,
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *sdk.CallToolRequest, input ReadDocumentInput) (*sdk.CallToolResult, ReadDocumentOutput, error) {
		toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
		defer cancel()
		snapshot, err := capturePDF(toolCtx, vault, gate, input, validator)
		if err != nil {
			return readDocumentError(err)
		}
		meta, err := bridge.Register(snapshot)
		if err != nil {
			_ = snapshot.Close()
			return readDocumentError(err)
		}
		out := ReadDocumentOutput{OK: true, Format: "pdf", MIMEType: documentMIMEPDF, RawBytes: snapshot.Size()}
		return &sdk.CallToolResult{Meta: meta}, out, nil
	}, summarizeReadDocumentArgs, summarizeReadDocumentResult)
}

func readDocumentError(err error) (*sdk.CallToolResult, ReadDocumentOutput, error) {
	code := errorCode(err)
	switch {
	case errors.Is(err, errDocumentBusy):
		code = "document_busy"
	case errors.Is(err, errDocumentMalformed), errors.Is(err, errDocumentEmpty):
		code = "malformed_document"
	case errors.Is(err, errDocumentUnsupported):
		code = UnsupportedFileCode
	case errors.Is(err, errDocumentTooLarge):
		code = string(fsx.CodeInputTooLarge)
	}
	return errorCallResult(), ReadDocumentOutput{OK: false, Error: &ToolError{Code: code, Message: documentErrorMessage(code)}}, nil
}

func documentErrorMessage(code string) string {
	switch code {
	case "document_busy":
		return "native document capacity is busy"
	case "malformed_document":
		return "document is malformed"
	default:
		return sanitizedMessage(code)
	}
}

func summarizeReadDocumentArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeArgs(builder, raw, []string{"path", "base"}, false)
}

func summarizeReadDocumentResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	var out ReadDocumentOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if out.RawBytes > 0 {
		if err := builder.Counter(localmcp.SectionResult, localmcp.CounterRawBytes, uint64(out.RawBytes)); err != nil {
			return err
		}
	}
	return summarizeErrorCode(builder, out.Error)
}

func capturePDF(ctx context.Context, vault *fsx.Vault, gate *documentByteGate, input ReadDocumentInput, validators ...DocumentValidator) (*documentSnapshot, error) {
	if !strings.EqualFold(filepath.Ext(input.Path), ".pdf") {
		return nil, errDocumentUnsupported
	}
	validator := DocumentValidator(DocumentValidatorFunc(validatePDF))
	if len(validators) > 0 && validators[0] != nil {
		validator = validators[0]
	}
	releaseValidator, err := validator.Reserve()
	if err != nil {
		return nil, err
	}
	defer releaseValidator()
	file, err := vault.OpenFile(ctx, input.Base, input.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	size := file.Resolved().Size
	switch {
	case size <= 0:
		return nil, errDocumentEmpty
	case size > DocumentMaxBytes:
		return nil, errDocumentTooLarge
	}
	release, err := gate.reserve(size)
	if err != nil {
		return nil, err
	}
	releaseOwned := true
	defer func() {
		if releaseOwned {
			release()
		}
	}()

	mapped, err := unix.Mmap(-1, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		return nil, err
	}
	mappedOwned := true
	defer func() {
		if mappedOwned {
			_ = unix.Munmap(mapped)
		}
	}()

	for offset := 0; offset < len(mapped); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(offset+documentChunkSize, len(mapped))
		n, readErr := file.Read(ctx, mapped[offset:end])
		offset += n
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if n == 0 && offset != len(mapped) {
			return nil, io.ErrUnexpectedEOF
		}
	}

	if err := validator.Validate(ctx, mapped); err != nil {
		return nil, err
	}
	if err := file.Revalidate(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uri, err := newDocumentURI()
	if err != nil {
		return nil, err
	}

	releaseOwned = false
	mappedOwned = false
	return &documentSnapshot{data: mapped, release: release, uri: uri}, nil
}

func validatePDF(_ context.Context, data []byte) error {
	return validatePDFReader(bytes.NewReader(data))
}

func validatePDFReader(reader io.ReadSeeker) error {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil || string(header[:]) != "%PDF-" {
		return errDocumentMalformed
	}
	end, err := reader.Seek(0, io.SeekEnd)
	if err != nil || end < 11 {
		return errDocumentMalformed
	}
	tailSize := min(end, int64(1024))
	if _, err := reader.Seek(-tailSize, io.SeekEnd); err != nil {
		return errDocumentMalformed
	}
	tail := make([]byte, tailSize)
	if _, err := io.ReadFull(reader, tail); err != nil || !bytes.HasSuffix(bytes.TrimSpace(tail), []byte("%%EOF")) {
		return errDocumentMalformed
	}
	marker := bytes.LastIndex(tail, []byte("startxref"))
	if marker < 0 {
		return errDocumentMalformed
	}
	value := bytes.TrimSpace(bytes.SplitN(tail[marker+len("startxref"):], []byte("%%EOF"), 2)[0])
	xrefOffset, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || xrefOffset < 0 || xrefOffset >= end {
		return errDocumentMalformed
	}
	if _, err := reader.Seek(xrefOffset, io.SeekStart); err != nil {
		return errDocumentMalformed
	}
	probe := make([]byte, min(int64(1024), end-xrefOffset))
	n, _ := io.ReadFull(reader, probe)
	probe = probe[:n]
	if !bytes.HasPrefix(probe, []byte("xref")) && !(bytes.Contains(probe, []byte(" obj")) && bytes.Contains(probe, []byte("/Type /XRef"))) {
		return errDocumentMalformed
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return errDocumentMalformed
	}
	disablePDFCPUConfig.Do(api.DisableConfigDir)
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	conf.Optimize = false
	conf.Offline = true
	if err := api.Validate(reader, conf); err != nil {
		return errDocumentMalformed
	}
	return nil
}

func newDocumentURI() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "obsidian://read-document/" + hex.EncodeToString(id[:]) + ".pdf", nil
}
