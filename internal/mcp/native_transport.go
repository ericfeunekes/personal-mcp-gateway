package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
			body, err := io.ReadAll(io.LimitReader(req.Body, limits.HTTPRequestBodyBytes+1))
			_ = req.Body.Close()
			if err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if int64(len(body)) > limits.HTTPRequestBodyBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			if rejectNativeDocumentBatch(body) != nil {
				http.Error(w, "read_document is not supported in JSON-RPC batches", http.StatusBadRequest)
				return
			}
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
		_ = writeNativeDocumentPayload(ctx, w, frame, payload)
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
