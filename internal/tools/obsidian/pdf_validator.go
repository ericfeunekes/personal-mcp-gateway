package obsidian

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

const pdfValidatorCommand = "internal-pdf-validator"

type DocumentValidator interface {
	Reserve() (func(), error)
	Validate(context.Context, []byte) error
}

type DocumentValidatorFunc func(context.Context, []byte) error

func (DocumentValidatorFunc) Reserve() (func(), error) { return func() {}, nil }
func (f DocumentValidatorFunc) Validate(ctx context.Context, data []byte) error {
	return f(ctx, data)
}

type pdfValidatorProcess struct {
	executable string
	slot       chan struct{}
}

// NewPDFValidatorProcess validates the captured anonymous snapshot in a
// bounded helper process. The helper accepts bytes only on stdin and exposes
// no path or generic host-access surface; its heap is reclaimed on exit. This
// is process-lifetime isolation, not a separate OS privilege sandbox: the
// helper inherits the gateway's user identity and environment.
func NewPDFValidatorProcess(executable string) DocumentValidator {
	return &pdfValidatorProcess{executable: executable, slot: make(chan struct{}, 1)}
}

func (v *pdfValidatorProcess) Reserve() (func(), error) {
	if v == nil || v.executable == "" || v.slot == nil {
		return nil, errDocumentMalformed
	}
	select {
	case v.slot <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-v.slot })
		}, nil
	default:
		return nil, errDocumentBusy
	}
}

func (v *pdfValidatorProcess) Validate(ctx context.Context, data []byte) error {
	cmd := exec.CommandContext(ctx, v.executable, pdfValidatorCommand, strconv.Itoa(len(data)))
	cmd.Stdin = &snapshotReader{data: data}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errDocumentMalformed
	}
	return nil
}

// RunPDFValidatorWorker handles the private bytes-only helper invocation.
func RunPDFValidatorWorker(args []string, input io.Reader) (bool, int) {
	if len(args) == 0 || args[0] != pdfValidatorCommand {
		return false, 0
	}
	if len(args) != 2 {
		return true, 2
	}
	size, err := strconv.Atoi(args[1])
	if err != nil || size <= 0 || int64(size) > DocumentMaxBytes {
		return true, 2
	}
	backing, err := os.CreateTemp("", "personal-mcp-gateway-pdf-validator-")
	if err != nil {
		return true, 1
	}
	name := backing.Name()
	if err := os.Remove(name); err != nil {
		_ = backing.Close()
		return true, 1
	}
	defer backing.Close()
	written, err := io.CopyN(backing, input, int64(size))
	if err != nil || written != int64(size) {
		return true, 1
	}
	var extra [1]byte
	if n, err := input.Read(extra[:]); n != 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return true, 1
	}
	if _, err := backing.Seek(0, io.SeekStart); err != nil || validatePDFReader(backing) != nil {
		return true, 1
	}
	return true, 0
}

type snapshotReader struct {
	data []byte
	off  int
}

func (r *snapshotReader) Read(p []byte) (int, error) {
	if r.off == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
