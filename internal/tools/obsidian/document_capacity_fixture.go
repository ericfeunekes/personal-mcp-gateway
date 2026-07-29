package obsidian

import (
	"bytes"
	"errors"
	"fmt"
)

const (
	DocumentFixtureMaxBytes = DocumentMaxBytes
	RejectedDocumentBytes   = DocumentMaxBytes + 1
	DocumentFixtureNonce    = "NDX-7Q4M-9K2P-R8VC"
	DocumentFixtureSHA256   = "9c063f55fb86cab8299f09ea85284b90ec06b37ce4ce46bbdb7fa6fc16ae0d0a"
)

func DocumentSizeAllowed(size int) bool { return size > 0 && int64(size) <= DocumentMaxBytes }

// GenerateDocumentCapacityFixture returns the exact synthetic PDF used by the
// local capacity gate. It is test/proof data and is never exposed as a tool.
func GenerateDocumentCapacityFixture() ([]byte, error) {
	return generateDocumentFixturePDF(DocumentFixtureMaxBytes)
}

func generateDocumentFixturePDF(target int) ([]byte, error) {
	if !DocumentSizeAllowed(target) {
		return nil, errors.New("document fixture exceeds its byte limit")
	}
	if target < 4096 {
		return nil, errors.New("document fixture target is too small")
	}
	filler := target - 2048
	for attempts := 0; attempts < 8; attempts++ {
		data := buildDocumentFixturePDF(filler)
		if len(data) == target {
			return data, nil
		}
		filler += target - len(data)
		if filler < 0 {
			return nil, errors.New("document fixture target cannot be generated")
		}
	}
	return nil, errors.New("document fixture size did not converge")
}

func buildDocumentFixturePDF(filler int) []byte {
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
		chunk := min(remaining, len(padding))
		_, _ = out.Write(padding[:chunk])
		remaining -= chunk
	}
	write("\nQ\nendstream\nendobj\n")
	object(5, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>")
	finalStream := "BT /F1 18 Tf 72 720 Td (Verification token: " + DocumentFixtureNonce + ") Tj ET\n" +
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
