package mcp

import (
	"bufio"
	"errors"
	"io"
)

var errNativeDocumentBatch = errors.New("read_document is not supported in JSON-RPC batches")

// nativeDocumentLineReader reads complete newline-delimited JSON-RPC frames so
// a native call inside a batch is rejected before the SDK sees any request.
type nativeDocumentLineReader struct {
	reader  *bufio.Reader
	closer  io.Closer
	max     int
	pending []byte
}

func newNativeDocumentLineReader(r io.ReadCloser, max int64) *nativeDocumentLineReader {
	return &nativeDocumentLineReader{reader: bufio.NewReader(r), closer: r, max: int(max)}
}

func (r *nativeDocumentLineReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		line := make([]byte, 0, min(r.max, 4096))
		for {
			fragment, err := r.reader.ReadSlice('\n')
			if len(line)+len(fragment) > r.max {
				return 0, errMessageTooLarge
			}
			line = append(line, fragment...)
			if err == nil || !errors.Is(err, bufio.ErrBufferFull) {
				if err != nil && len(line) == 0 {
					return 0, err
				}
				break
			}
		}
		if rejectNativeDocumentBatch(line) != nil {
			return 0, errNativeDocumentBatch
		}
		r.pending = line
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *nativeDocumentLineReader) Close() error { return r.closer.Close() }
