package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"personal-mcp-gateway/internal/limits"
)

const (
	nativeDocumentResponseDeadline = 30 * time.Second
	nativeDocumentMarkerBytes      = limits.StdioMessageBytes
)

type nativeDocumentWriter struct {
	ctx    context.Context
	writer io.Writer
	bridge *NativeDocumentBridge
	closer io.Closer
}

func (w *nativeDocumentWriter) Write(p []byte) (int, error) {
	if bounded, ok := w.writer.(*pollWriteCloser); ok {
		bounded.beginResponse()
		defer bounded.endResponse()
	}
	if marked, err := writeNativeDocument(w.ctx, w.writer, bytes.TrimSuffix(p, []byte("\n")), w.bridge); marked {
		if err != nil {
			return 0, err
		}
		_, err = w.writer.Write([]byte("\n"))
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return w.writer.Write(p)
}

func (w *nativeDocumentWriter) Close() error {
	if w.closer != nil {
		return w.closer.Close()
	}
	return nil
}

type nativeDocumentHTTPRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

// nativeDocumentRequestBody observes the SDK's sole request-body read so the
// native-document batch restriction remains pre-dispatch without replacing the
// request body after a second full read in this wrapper.
type nativeDocumentRequestBody struct {
	io.ReadCloser
	body    bytes.Buffer
	pending error
}

func (r *nativeDocumentRequestBody) Read(p []byte) (int, error) {
	if r.pending != nil {
		return 0, r.pending
	}
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.body.Write(p[:n])
	}
	if err != io.EOF {
		return n, err
	}
	if rejectNativeDocumentBatch(r.body.Bytes()) == nil {
		return n, err
	}
	r.pending = errNativeDocumentBatch
	if n == 0 {
		return 0, r.pending
	}
	return n, nil
}

func newNativeDocumentHTTPRecorder() *nativeDocumentHTTPRecorder {
	return &nativeDocumentHTTPRecorder{header: make(http.Header)}
}
func (r *nativeDocumentHTTPRecorder) Header() http.Header { return r.header }
func (r *nativeDocumentHTTPRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *nativeDocumentHTTPRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if int64(r.body.Len()+len(p)) > nativeDocumentMarkerBytes {
		r.err = errMessageTooLarge
		return 0, r.err
	}
	return r.body.Write(p)
}

func (r *nativeDocumentHTTPRecorder) copyTo(w http.ResponseWriter) {
	for key, values := range r.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	if r.status != 0 {
		w.WriteHeader(r.status)
	}
	_, _ = w.Write(r.body.Bytes())
}

func nativeDocumentHTTPHandler(next http.Handler, bridge *NativeDocumentBridge) http.Handler {
	if bridge == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && req.Body != nil {
			req.Body = &nativeDocumentRequestBody{ReadCloser: req.Body}
		}
		recorder := newNativeDocumentHTTPRecorder()
		next.ServeHTTP(recorder, req)
		if recorder.err != nil {
			http.Error(w, "MCP response too large", http.StatusInternalServerError)
			return
		}
		frame := recorder.body.Bytes()
		token, marked := nativeDocumentToken(frame)
		if !marked {
			recorder.copyTo(w)
			return
		}
		payload, err := bridge.take(token)
		if err != nil {
			http.Error(w, "native document payload is unavailable", http.StatusInternalServerError)
			return
		}
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(nativeDocumentResponseDeadline)); err != nil {
			_ = payload.Close()
			http.Error(w, "native document response deadline is unavailable", http.StatusInternalServerError)
			return
		}
		for key, values := range recorder.header {
			w.Header()[key] = append([]string(nil), values...)
		}
		if recorder.status != 0 {
			w.WriteHeader(recorder.status)
		}
		ctx, cancel := context.WithTimeout(req.Context(), nativeDocumentResponseDeadline)
		defer cancel()
		if err := writeNativeDocumentPayload(ctx, w, frame, payload); err != nil {
			// The HTTP status may already have been committed, so retain a
			// sanitized operational failure signal without recording request,
			// path, token, payload, or returned bytes.
			slog.Error("native document response write failed")
		}
	})
}

func nativeDocumentBatch(data []byte) bool {
	var batch []struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(data, &batch) != nil {
		return false
	}
	for _, request := range batch {
		if request.Method == "tools/call" && request.Params.Name == "read_document" {
			return true
		}
	}
	return false
}

func rejectNativeDocumentBatch(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '[' {
		return nil
	}
	if nativeDocumentBatch(data) {
		return errNativeDocumentBatch
	}
	return nil
}
