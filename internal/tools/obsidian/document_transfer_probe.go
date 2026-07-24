package obsidian

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "personal-mcp-gateway/internal/mcp"
)

const (
	ToolDocumentTransferProbe        = "document_transfer_probe"
	DocumentTransferProbeDescription = "Return one synthetic PDF as native embedded-resource bytes. This temporary read-only tool exists only for the approved issue #4 transport probe; it accepts no arguments and does not read the vault."
	DocumentTransferProbeMIME        = "application/pdf"
	DocumentTransferProbeURI         = "obsidian://probe/native-document-transfer.pdf"
	MaxDocumentTransferProbeBytes    = 49_999_999
	RejectedDocumentTransferBytes    = 50_000_000
	DocumentTransferProbeNonce       = "NDX-7Q4M-9K2P-R8VC"
	DocumentTransferProbeSHA256      = "935d656fc945ea0c002af42af4ae62edfbf48f22aeb03872c7a4b51fe6beb69d"
	documentTransferBaseSHA256       = "f8005c1abc16d9be4631d22d3a07e242f39a837187482669343d448b99067cc4"

	documentTransferProbePDFBase64 = "JVBERi0xLjQKJZOMi54gUmVwb3J0TGFiIEdlbmVyYXRlZCBQREYgZG9jdW1lbnQgKG9wZW5zb3VyY2UpCjEgMCBvYmoKPDwKL0YxIDIgMCBSIC9GMiAzIDAgUgo+PgplbmRvYmoKMiAwIG9iago8PAovQmFzZUZvbnQgL0hlbHZldGljYSAvRW5jb2RpbmcgL1dpbkFuc2lFbmNvZGluZyAvTmFtZSAvRjEgL1N1YnR5cGUgL1R5cGUxIC9UeXBlIC9Gb250Cj4+CmVuZG9iagozIDAgb2JqCjw8Ci9CYXNlRm9udCAvSGVsdmV0aWNhLUJvbGQgL0VuY29kaW5nIC9XaW5BbnNpRW5jb2RpbmcgL05hbWUgL0YyIC9TdWJ0eXBlIC9UeXBlMSAvVHlwZSAvRm9udAo+PgplbmRvYmoKNCAwIG9iago8PAovQ29udGVudHMgOCAwIFIgL01lZGlhQm94IFsgMCAwIDYxMiA3OTIgXSAvUGFyZW50IDcgMCBSIC9SZXNvdXJjZXMgPDwKL0ZvbnQgMSAwIFIgL1Byb2NTZXQgWyAvUERGIC9UZXh0IC9JbWFnZUIgL0ltYWdlQyAvSW1hZ2VJIF0KPj4gL1JvdGF0ZSAwIC9UcmFucyA8PAoKPj4gCiAgL1R5cGUgL1BhZ2UKPj4KZW5kb2JqCjUgMCBvYmoKPDwKL1BhZ2VNb2RlIC9Vc2VOb25lIC9QYWdlcyA3IDAgUiAvVHlwZSAvQ2F0YWxvZwo+PgplbmRvYmoKNiAwIG9iago8PAovQXV0aG9yIChhbm9ueW1vdXMpIC9DcmVhdGlvbkRhdGUgKEQ6MjAyNjA3MjExOTQ3MDgtMDMnMDAnKSAvQ3JlYXRvciAoYW5vbnltb3VzKSAvS2V5d29yZHMgKCkgL01vZERhdGUgKEQ6MjAyNjA3MjExOTQ3MDgtMDMnMDAnKSAvUHJvZHVjZXIgKFJlcG9ydExhYiBQREYgTGlicmFyeSAtIFwob3BlbnNvdXJjZVwpKSAKICAvU3ViamVjdCAodW5zcGVjaWZpZWQpIC9UaXRsZSAoTmF0aXZlIGRvY3VtZW50IHRyYW5zZmVyIHByb2JlKSAvVHJhcHBlZCAvRmFsc2UKPj4KZW5kb2JqCjcgMCBvYmoKPDwKL0NvdW50IDEgL0tpZHMgWyA0IDAgUiBdIC9UeXBlIC9QYWdlcwo+PgplbmRvYmoKOCAwIG9iago8PAovTGVuZ3RoIDYwOQo+PgpzdHJlYW0KMSAwIDAgMSAwIDAgY20gIEJUIC9GMSAxMiBUZiAxNC40IFRMIEVUCkJUIC9GMiAxOCBUZiAyMS42IFRMIEVUCkJUIDEgMCAwIDEgNzIgNzIwIFRtIChOYXRpdmUgZG9jdW1lbnQgdHJhbnNmZXIgcHJvYmUpIFRqIFQqIEVUCkJUIC9GMSAxMiBUZiAxNC40IFRMIEVUCkJUIDEgMCAwIDEgNzIgNjkwIFRtIChWZXJpZmljYXRpb24gdG9rZW46IE5EWC03UTRNLTlLMlAtUjhWQykgVGogVCogRVQKLjE0MTE3NiAuNDE5NjA4IC45OTIxNTcgcmcKbgoyMTggNTQwIG0KMjE4IDU2Ni41MDk3IDE5Ni41MDk3IDU4OCAxNzAgNTg4IGMKMTQzLjQ5MDMgNTg4IDEyMiA1NjYuNTA5NyAxMjIgNTQwIGMKMTIyIDUxMy40OTAzIDE0My40OTAzIDQ5MiAxNzAgNDkyIGMKMTk2LjUwOTcgNDkyIDIxOCA1MTMuNDkwMyAyMTggNTQwIGMKZioKLjk0OTAyIC41NDkwMiAuMTU2ODYzIHJnCm4gMzUwIDQ5MiA5NiA5NiByZSBmKgouMDY2NjY3IC4wNjY2NjcgLjA2NjY2NyByZwpCVCAvRjEgMTEgVGYgMTMuMiBUTCBFVApCVCAxIDAgMCAxIDE0NS4yNDQ1IDQ2NSBUbSAoYmx1ZSBjaXJjbGUpIFRqIFQqIEVUCkJUIDEgMCAwIDEgMzYyLjUzNiA0NjUgVG0gKG9yYW5nZSBzcXVhcmUpIFRqIFQqIEVUCiAKZW5kc3RyZWFtCmVuZG9iagp4cmVmCjAgOQowMDAwMDAwMDAwIDY1NTM1IGYgCjAwMDAwMDAwNjEgMDAwMDAgbiAKMDAwMDAwMDEwMiAwMDAwMCBuIAowMDAwMDAwMjA5IDAwMDAwIG4gCjAwMDAwMDAzMjEgMDAwMDAgbiAKMDAwMDAwMDUxNCAwMDAwMCBuIAowMDAwMDAwNTgyIDAwMDAwIG4gCjAwMDAwMDA4NjUgMDAwMDAgbiAKMDAwMDAwMDkyNCAwMDAwMCBuIAp0cmFpbGVyCjw8Ci9JRCAKWzxlNDMzMTRhYWQ4MDhlMTgwOGQ4ZDU4ZmIxZmMyMWE1NT48ZTQzMzE0YWFkODA4ZTE4MDhkOGQ1OGZiMWZjMjFhNTU+XQolIFJlcG9ydExhYiBnZW5lcmF0ZWQgUERGIGRvY3VtZW50IC0tIGRpZ2VzdCAob3BlbnNvdXJjZSkKCi9JbmZvIDYgMCBSCi9Sb290IDUgMCBSCi9TaXplIDkKPj4Kc3RhcnR4cmVmCjE1ODMKJSVFT0YK"
)

// Set only by the disposable candidate build. Normal builds and tests retain
// the accepted five-tool surface.
var documentTransferProbeBuild = "disabled"

var documentTransferProbePDF = mustDecodeDocumentTransferProbePDF()

type DocumentTransferProbeInput struct{}

type DocumentTransferProbeOutput struct {
	MIMEType string `json:"mime_type"`
	RawBytes int    `json:"raw_bytes"`
}

func documentTransferProbeEnabled() bool {
	return documentTransferProbeBuild == "enabled" || documentTransferCapacityEnabled()
}

func documentTransferCapacityEnabled() bool {
	return documentTransferProbeBuild == "capacity"
}

func DocumentTransferProbeExpectedSHA256() string {
	if documentTransferCapacityEnabled() {
		return DocumentTransferProbeSHA256
	}
	return documentTransferBaseSHA256
}

func DocumentTransferProbeSHA256ForSize(size int) (string, bool) {
	switch size {
	case len(documentTransferProbePDF):
		return documentTransferBaseSHA256, true
	case MaxDocumentTransferProbeBytes:
		return DocumentTransferProbeSHA256, true
	default:
		return "", false
	}
}

func mustDecodeDocumentTransferProbePDF() []byte {
	data, err := base64.StdEncoding.DecodeString(documentTransferProbePDFBase64)
	if err != nil {
		panic("invalid native document transfer probe fixture")
	}
	return data
}

func validateDocumentTransferProbeBytes(data []byte) error {
	return validateDocumentTransferProbeSize(len(data))
}

func validateDocumentTransferProbeSize(size int) error {
	if size == 0 || size > MaxDocumentTransferProbeBytes {
		return errors.New("native document transfer probe exceeds its byte limit")
	}
	return nil
}

// DocumentTransferProbeSizeAllowed is disposable spike plumbing used by the
// capacity report to bind its recorded boundary decision to the handler's
// exact validator.
func DocumentTransferProbeSizeAllowed(size int) bool {
	return validateDocumentTransferProbeSize(size) == nil
}

func generateDocumentTransferProbePDF(target int) ([]byte, error) {
	if err := validateDocumentTransferProbeSize(target); err != nil {
		return nil, err
	}
	if target < 4096 {
		return nil, errors.New("native document transfer probe target is too small")
	}
	filler := target - 2048
	for attempts := 0; attempts < 8; attempts++ {
		data := buildDocumentTransferProbePDF(filler)
		if len(data) == target {
			return data, nil
		}
		filler += target - len(data)
		if filler < 0 {
			return nil, errors.New("native document transfer probe target cannot be generated")
		}
	}
	return nil, errors.New("native document transfer probe size did not converge")
}

// GenerateDocumentTransferProbePDFForSpike returns the frozen capacity
// fixture for independent pre-measurement validation. It is temporary spike
// plumbing, not an MCP capability.
func GenerateDocumentTransferProbePDFForSpike() ([]byte, error) {
	return generateDocumentTransferProbePDF(MaxDocumentTransferProbeBytes)
}

func buildDocumentTransferProbePDF(filler int) []byte {
	var out bytes.Buffer
	out.Grow(filler + 2048)
	offsets := make([]int, 8)
	write := func(value string) { _, _ = out.WriteString(value) }
	object := func(number int, body string) {
		offsets[number] = out.Len()
		write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", number, body))
	}
	write("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	object(2, "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>")
	object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 7 0 R >> >> >>")
	firstStreamLength := filler + len("q\n") + len("\nQ\n")
	offsets[4] = out.Len()
	write(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\nq\n", firstStreamLength))
	padding := []byte("                                                                ")
	for remaining := filler; remaining > 0; {
		chunk := remaining
		if chunk > len(padding) {
			chunk = len(padding)
		}
		_, _ = out.Write(padding[:chunk])
		remaining -= chunk
	}
	write("\nQ\nendstream\nendobj\n")
	object(5, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>")
	finalStream := "BT /F1 18 Tf 72 720 Td (Verification token: " + DocumentTransferProbeNonce + ") Tj ET\n" +
		"0.141176 0.419608 0.992157 rg\n218 540 m 218 566.51 196.51 588 170 588 c 143.49 588 122 566.51 122 540 c 122 513.49 143.49 492 170 492 c 196.51 492 218 513.49 218 540 c f\n" +
		"0.94902 0.54902 0.156863 rg\n350 492 96 96 re f\n"
	offsets[6] = out.Len()
	write(fmt.Sprintf("6 0 obj\n<< /Length %d >>\nstream\n%sends", len(finalStream), finalStream))
	write("tream\nendobj\n")
	object(7, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xref := out.Len()
	write("xref\n0 8\n0000000000 65535 f \n")
	for number := 1; number <= 7; number++ {
		write(fmt.Sprintf("%010d 00000 n \n", offsets[number]))
	}
	write(fmt.Sprintf("trailer\n<< /Size 8 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref))
	return out.Bytes()
}

func documentTransferProbeDescriptor(tools *Tools) (localmcp.ToolDescriptor, error) {
	return localmcp.NewToolDescriptor(sdk.Tool{
		Name:        ToolDocumentTransferProbe,
		Description: DocumentTransferProbeDescription,
		Annotations: readOnlyToolAnnotations(),
	}, tools.DocumentTransferProbe, summarizeDocumentTransferProbeArgs, summarizeDocumentTransferProbeResult)
}

func (t *Tools) DocumentTransferProbe(context.Context, *sdk.CallToolRequest, DocumentTransferProbeInput) (*sdk.CallToolResult, DocumentTransferProbeOutput, error) {
	data := documentTransferProbePDF
	var err error
	if documentTransferCapacityEnabled() {
		data, err = generateDocumentTransferProbePDF(MaxDocumentTransferProbeBytes)
	}
	if err == nil {
		err = validateDocumentTransferProbeBytes(data)
	}
	if err != nil {
		return nil, DocumentTransferProbeOutput{}, err
	}
	data = append([]byte(nil), data...)
	return &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
			URI:      DocumentTransferProbeURI,
			MIMEType: DocumentTransferProbeMIME,
			Blob:     data,
		}},
	}}, DocumentTransferProbeOutput{MIMEType: DocumentTransferProbeMIME, RawBytes: len(data)}, nil
}

func summarizeDocumentTransferProbeArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return builder.Enum(localmcp.SectionArguments, localmcp.EnumShape, localmcp.ValueInvalidJSON)
	}
	return builder.UnknownArgumentKeys(object)
}

func summarizeDocumentTransferProbeResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	var out DocumentTransferProbeOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	return builder.Counter(localmcp.SectionResult, localmcp.CounterRawBytes, uint64(out.RawBytes))
}
