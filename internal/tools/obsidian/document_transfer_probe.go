package obsidian

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "personal-mcp-gateway/internal/mcp"
)

const (
	ToolDocumentTransferProbe        = "document_transfer_probe"
	DocumentTransferProbeDescription = "Return one synthetic PDF as native embedded-resource bytes. This temporary read-only tool exists only for the approved issue #4 transport probe; it accepts no arguments and does not read the vault."
	DocumentTransferProbeMIME        = "application/pdf"
	DocumentTransferProbeURI         = "obsidian://probe/native-document-transfer.pdf"
	MaxDocumentTransferProbeBytes    = 512 * 1024

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
	return documentTransferProbeBuild == "enabled"
}

func mustDecodeDocumentTransferProbePDF() []byte {
	data, err := base64.StdEncoding.DecodeString(documentTransferProbePDFBase64)
	if err != nil {
		panic("invalid native document transfer probe fixture")
	}
	return data
}

func validateDocumentTransferProbeBytes(data []byte) error {
	if len(data) == 0 || len(data) > MaxDocumentTransferProbeBytes {
		return errors.New("native document transfer probe exceeds its byte limit")
	}
	return nil
}

func documentTransferProbeDescriptor(tools *Tools) (localmcp.ToolDescriptor, error) {
	return localmcp.NewToolDescriptor(sdk.Tool{
		Name:        ToolDocumentTransferProbe,
		Description: DocumentTransferProbeDescription,
		Annotations: readOnlyToolAnnotations(),
	}, tools.DocumentTransferProbe, summarizeDocumentTransferProbeArgs, summarizeDocumentTransferProbeResult)
}

func (t *Tools) DocumentTransferProbe(context.Context, *sdk.CallToolRequest, DocumentTransferProbeInput) (*sdk.CallToolResult, DocumentTransferProbeOutput, error) {
	if err := validateDocumentTransferProbeBytes(documentTransferProbePDF); err != nil {
		return nil, DocumentTransferProbeOutput{}, err
	}
	data := append([]byte(nil), documentTransferProbePDF...)
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
