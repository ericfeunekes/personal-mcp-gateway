package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/resourceprobe"
	"personal-mcp-gateway/internal/tools/obsidian"
)

const (
	documentTransferCapacitySchema         = "personal-mcp-gateway.document-transfer-capacity.v4"
	documentTransferCapacityVersion        = 4
	documentTransferAggregateRSSLimitBytes = int64(160 * 1024 * 1024)
	documentBackpressureLowerBound         = 29 * time.Second
	documentBackpressureUpperBound         = 34 * time.Second
	documentSerializationFailureSignal     = "native document response write failed"
)

type documentTransferCheckpoint struct {
	ElapsedMicroseconds int64                `json:"elapsed_microseconds"`
	Memory              resourceMemoryReport `json:"memory"`
	RSSBytes            int64                `json:"rss_bytes"`
	CPUTimeMicroseconds int64                `json:"cpu_time_microseconds"`
	FDCount             int                  `json:"fd_count"`
}

type documentTransferCapacityReport struct {
	ReportKind                         string                       `json:"report_kind"`
	ReportSchema                       string                       `json:"report_schema"`
	SchemaVersion                      int                          `json:"schema_version"`
	Passed                             bool                         `json:"passed"`
	CandidateCommit                    string                       `json:"candidate_commit"`
	CandidateSHA256                    string                       `json:"candidate_sha256"`
	DependencySHA256                   string                       `json:"dependency_sha256"`
	CandidateRuntime                   candidateRuntimeProfile      `json:"candidate_runtime"`
	Machine                            machineProfile               `json:"machine"`
	DescriptorCount                    int                          `json:"descriptor_count"`
	RawBytes                           int                          `json:"raw_bytes"`
	RawSHA256                          string                       `json:"raw_sha256"`
	EncodedWireBytes                   int64                        `json:"encoded_wire_bytes"`
	EncodedWireFrames                  int64                        `json:"encoded_wire_frames"`
	StructuredResultBytes              int                          `json:"structured_result_bytes"`
	CallLatencyMicroseconds            int64                        `json:"call_latency_microseconds"`
	SequentialCallCount                int                          `json:"sequential_call_count"`
	CallWithinTwoSeconds               bool                         `json:"call_within_two_seconds"`
	NonceOffset                        int                          `json:"nonce_offset"`
	FinalPageObjectOffset              int                          `json:"final_page_object_offset"`
	VisualEvidenceOffset               int                          `json:"visual_evidence_offset"`
	TerminalEvidenceWithin4096Bytes    bool                         `json:"terminal_evidence_within_4096_bytes"`
	ArtifactSHA256                     string                       `json:"artifact_sha256"`
	PDFValidatorAccepted               bool                         `json:"pdf_validator_accepted"`
	TerminalTextExtractionPassed       bool                         `json:"terminal_text_extraction_passed"`
	RenderedFinalPageSHA256            string                       `json:"rendered_final_page_sha256"`
	RenderedGeometryPassed             bool                         `json:"rendered_geometry_passed"`
	FixtureLeakageCheckPassed          bool                         `json:"fixture_leakage_check_passed"`
	FirstDisallowedSizeRejected        bool                         `json:"first_disallowed_size_rejected"`
	Baseline                           documentTransferCheckpoint   `json:"baseline"`
	PostCall                           []documentTransferCheckpoint `json:"post_call"`
	GCAcknowledgementCount             int                          `json:"gc_acknowledgement_count"`
	HighWaterRSSBytes                  int64                        `json:"high_water_rss_bytes"`
	HighWaterRSSDeltaBytes             int64                        `json:"high_water_rss_delta_bytes"`
	HighWaterWithinBound               bool                         `json:"high_water_within_bound"`
	ValidatorHighWaterRSSBytes         int64                        `json:"validator_high_water_rss_bytes"`
	AggregateHighWaterUpperBoundBytes  int64                        `json:"aggregate_high_water_upper_bound_bytes"`
	AggregateHighWaterWithinBound      bool                         `json:"aggregate_high_water_within_bound"`
	RetainedHeapAllocGrowthBytes       uint64                       `json:"retained_heap_alloc_growth_bytes"`
	RetainedHeapAllocGrowthWithinBound bool                         `json:"retained_heap_alloc_growth_within_bound"`
	RetainedRSSWindowGrowthBytes       int64                        `json:"retained_rss_window_growth_bytes"`
	RetainedRSSWindowGrowthWithinBound bool                         `json:"retained_rss_window_growth_within_bound"`
	AllFDsRecovered                    bool                         `json:"all_fds_recovered"`
	ActivityQuiescent                  bool                         `json:"activity_quiescent"`
	FollowupSucceeded                  bool                         `json:"followup_succeeded"`
	Idle                               idleResourceReport           `json:"idle"`
	HTTP                               documentTransferHTTPReport   `json:"http"`
}

type documentTransferHTTPReport struct {
	DescriptorCount              int    `json:"descriptor_count"`
	SequentialCallCount          int    `json:"sequential_call_count"`
	RawBytes                     int    `json:"raw_bytes"`
	RawSHA256                    string `json:"raw_sha256"`
	MaxCallLatencyMicroseconds   int64  `json:"max_call_latency_microseconds"`
	EveryCallWithinTwoSeconds    bool   `json:"every_call_within_two_seconds"`
	FollowupSucceeded            bool   `json:"followup_succeeded"`
	BaselineRSSBytes             int64  `json:"baseline_rss_bytes"`
	PostCallRSSBytes             int64  `json:"post_call_rss_bytes"`
	RetainedRSSGrowthBytes       int64  `json:"retained_rss_growth_bytes"`
	RetainedRSSGrowthWithinBound bool   `json:"retained_rss_growth_within_bound"`
	HighWaterRSSBytes            int64  `json:"high_water_rss_bytes"`
	HighWaterRSSDeltaBytes       int64  `json:"high_water_rss_delta_bytes"`
	HighWaterWithinBound         bool   `json:"high_water_within_bound"`
	BaselineFDCount              int    `json:"baseline_fd_count"`
	PostCallFDCount              int    `json:"post_call_fd_count"`
	AllFDsRecovered              bool   `json:"all_fds_recovered"`
	SQLiteToolCallRows           int    `json:"sqlite_tool_call_rows"`
	SQLiteTelemetryValidated     bool   `json:"sqlite_telemetry_validated"`
	FirstDisallowedSizeRejected  bool   `json:"first_disallowed_size_rejected"`
	NegativeCallCount            int    `json:"negative_call_count"`
	NegativeMatrixPassed         bool   `json:"negative_matrix_passed"`
	VaultUnchanged               bool   `json:"vault_unchanged"`
	ConcurrentAdmissionPassed    bool   `json:"concurrent_admission_passed"`
	BusyErrorSanitized           bool   `json:"busy_error_sanitized"`
	RetryAfterBusySucceeded      bool   `json:"retry_after_busy_succeeded"`
	BackpressureDeadlinePassed   bool   `json:"backpressure_deadline_passed"`
	BackpressureElapsedMicros    int64  `json:"backpressure_elapsed_microseconds"`
	BackpressureRetrySucceeded   bool   `json:"backpressure_retry_succeeded"`
	SerializationFailureObserved bool   `json:"serialization_failure_observed"`
	TelemetryPrivacyValidated    bool   `json:"telemetry_privacy_validated"`
	TelemetryValidationCode      string `json:"telemetry_validation_code"`
}

type stallingRoundTripper struct {
	base    http.RoundTripper
	mu      sync.Mutex
	armed   bool
	started chan struct{}
	release chan struct{}
}

func (t *stallingRoundTripper) arm() (<-chan struct{}, chan<- struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.armed = true
	t.started = make(chan struct{})
	t.release = make(chan struct{})
	return t.started, t.release
}

func (t *stallingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	armed := t.armed && request.Method == http.MethodPost && bytes.Contains(body, []byte(`"name":"read_document"`))
	if armed {
		t.armed = false
		started, release := t.started, t.release
		response.Body = &stalledResponseBody{ReadCloser: response.Body, started: started, release: release}
	}
	t.mu.Unlock()
	return response, nil
}

type stalledResponseBody struct {
	io.ReadCloser
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type signalingBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	signal chan struct{}
	once   sync.Once
}

func newSignalingBuffer() *signalingBuffer {
	return &signalingBuffer{signal: make(chan struct{})}
}

func (b *signalingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buffer.Write(p)
	matched := strings.Contains(b.buffer.String(), documentSerializationFailureSignal)
	b.mu.Unlock()
	if matched {
		b.once.Do(func() { close(b.signal) })
	}
	return n, err
}

func (b *signalingBuffer) containsSignal() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.buffer.String(), documentSerializationFailureSignal)
}

func (b *stalledResponseBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.ReadCloser.Read(p)
}

type countingTransport struct {
	command *exec.Cmd
	read    atomic.Int64
	frames  atomic.Int64
}

func (t *countingTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	stdout, err := t.command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := t.command.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := t.command.Start(); err != nil {
		return nil, err
	}
	return &countingConnection{
		reader: bufio.NewReader(stdout), stdout: stdout, stdin: stdin, command: t.command,
		read: &t.read, frames: &t.frames, closed: make(chan struct{}),
	}, nil
}

type countingConnection struct {
	reader    *bufio.Reader
	stdout    io.ReadCloser
	stdin     io.WriteCloser
	command   *exec.Cmd
	read      *atomic.Int64
	frames    *atomic.Int64
	readMu    sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
}

func (c *countingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	frame, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	c.read.Add(int64(len(frame)))
	c.frames.Add(1)
	return jsonrpc.DecodeMessage(bytes.TrimSuffix(frame, []byte{'\n'}))
}

func (c *countingConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	encoded, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(encoded)
	return err
}

func (c *countingConnection) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- c.command.Wait() }()
		select {
		case c.closeErr = <-done:
		case <-time.After(2 * time.Second):
			if signalErr := c.command.Process.Signal(syscall.SIGTERM); signalErr != nil {
				_ = c.command.Process.Kill()
			}
			select {
			case c.closeErr = <-done:
			case <-time.After(2 * time.Second):
				_ = c.command.Process.Kill()
				c.closeErr = <-done
			}
		}
		_ = c.stdout.Close()
		close(c.closed)
	})
	return c.closeErr
}

func (*countingConnection) SessionID() string { return "" }

func connectCountingResourceCandidate(ctx context.Context, gatewayBin, root string) (*resourceCandidate, *countingTransport, error) {
	dbPath, cleanup, err := newPrivateSQLiteStore()
	if err != nil {
		return nil, nil, err
	}
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		cleanup()
		return nil, nil, errors.New("document transfer resource control setup failed")
	}
	ackRead, ackWrite, err := os.Pipe()
	if err != nil {
		_ = commandRead.Close()
		_ = commandWrite.Close()
		cleanup()
		return nil, nil, errors.New("document transfer resource control setup failed")
	}
	closeAll := func() {
		_ = commandRead.Close()
		_ = commandWrite.Close()
		_ = ackRead.Close()
		_ = ackWrite.Close()
	}
	cmd := exec.Command(gatewayBin, "stdio", "--obsidian-root", root, "--telemetry-db", dbPath)
	cmd.ExtraFiles = []*os.File{commandRead, ackWrite}
	cmd.Env = environmentWithOverride(os.Environ(), resourceprobe.Environment, "3,4")
	cmd.Stderr = io.Discard
	transport := &countingTransport{command: cmd}
	client := sdk.NewClient(&sdk.Implementation{Name: "document-transfer-capacity", Version: "v1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	_ = commandRead.Close()
	_ = ackWrite.Close()
	if err != nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		closeAll()
		cleanup()
		return nil, nil, errors.New("document transfer candidate connection failed")
	}
	return &resourceCandidate{
		process: &candidateProcess{session: session, command: cmd}, dbPath: dbPath, cleanup: cleanup,
		control: &resourceControl{command: commandWrite, ack: ackRead, reader: bufio.NewReaderSize(ackRead, resourceAckMaxBytes)},
	}, transport, nil
}

func documentTransferCheckpointFrom(elapsed time.Duration, memory resourceMemorySnapshot, sample processResourceSample) documentTransferCheckpoint {
	return documentTransferCheckpoint{
		ElapsedMicroseconds: elapsed.Microseconds(),
		Memory: resourceMemoryReport{
			HeapAllocBytes: memory.heapAlloc, HeapInuseBytes: memory.heapInuse, HeapObjects: memory.heapObjects,
			HeapReleasedBytes: memory.heapReleased, HeapSysBytes: memory.heapSys,
		},
		RSSBytes: sample.rssBytes, CPUTimeMicroseconds: sample.cpuMicros, FDCount: sample.fdCount,
	}
}

func observeDocumentTransferCheckpoint(ctx context.Context, started time.Time, candidate *resourceCandidate, sampler resourceSampler) (documentTransferCheckpoint, error) {
	memory, err := candidate.control.gc(ctx, resourceControlTime)
	if err != nil {
		return documentTransferCheckpoint{}, err
	}
	sample, err := sampler.Sample(ctx, candidate.process.command.Process.Pid, true)
	if err != nil {
		return documentTransferCheckpoint{}, err
	}
	return documentTransferCheckpointFrom(time.Since(started), memory, sample), nil
}

type documentTransferCallEvidence struct {
	rawBytes          int
	rawSHA256         string
	encodedWireBytes  int64
	encodedWireFrames int64
	structuredBytes   int
	latency           time.Duration
	nonceOffset       int
	finalPageOffset   int
	visualOffset      int
	terminalEvidence  bool
}

type documentTransferArtifactEvidence struct {
	artifactSHA256               string
	pdfValidatorAccepted         bool
	terminalTextExtractionPassed bool
	renderedFinalPageSHA256      string
	renderedGeometryPassed       bool
	fixtureLeakageCheckPassed    bool
}

func commandOutput(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, arguments...).Output()
	if err != nil {
		return nil, errors.New("document transfer artifact validation failed")
	}
	return output, nil
}

func validateDocumentTransferArtifact(ctx context.Context, artifactPath, expectedSHA string) (documentTransferArtifactEvidence, error) {
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return documentTransferArtifactEvidence{}, errors.New("document transfer artifact validation failed")
	}
	digest := sha256.Sum256(data)
	evidence := documentTransferArtifactEvidence{artifactSHA256: hex.EncodeToString(digest[:])}
	if evidence.artifactSHA256 != expectedSHA || len(data) != obsidian.DocumentFixtureMaxBytes {
		return evidence, errors.New("document transfer artifact identity changed")
	}
	info, err := commandOutput(ctx, "pdfinfo", artifactPath)
	if err != nil || !strings.Contains(string(info), "Pages:           2") {
		return evidence, errors.New("document transfer PDF validator failed")
	}
	evidence.pdfValidatorAccepted = true
	firstText, err := commandOutput(ctx, "pdftotext", "-f", "1", "-l", "1", artifactPath, "-")
	if err != nil {
		return evidence, err
	}
	finalText, err := commandOutput(ctx, "pdftotext", "-f", "2", "-l", "2", artifactPath, "-")
	if err != nil {
		return evidence, err
	}
	evidence.terminalTextExtractionPassed = !bytes.Contains(firstText, []byte(obsidian.DocumentFixtureNonce)) &&
		bytes.Count(finalText, []byte(obsidian.DocumentFixtureNonce)) == 1
	if !evidence.terminalTextExtractionPassed {
		return evidence, errors.New("document transfer terminal text evidence failed")
	}
	lowerData := bytes.ToLower(data)
	evidence.fixtureLeakageCheckPassed = true
	for _, forbidden := range [][]byte{[]byte("blue circle"), []byte("orange square"), []byte("circle is left"), []byte("square is right")} {
		if bytes.Contains(lowerData, forbidden) {
			evidence.fixtureLeakageCheckPassed = false
		}
	}
	if !evidence.fixtureLeakageCheckPassed {
		return evidence, errors.New("document transfer fixture leaked its visual answer")
	}
	renderPrefix := artifactPath + ".page-2"
	if _, err := commandOutput(ctx, "pdftoppm", "-f", "2", "-l", "2", "-singlefile", "-png", "-r", "72", artifactPath, renderPrefix); err != nil {
		return evidence, err
	}
	renderPath := renderPrefix + ".png"
	rendered, err := os.ReadFile(renderPath)
	if err != nil {
		return evidence, errors.New("document transfer final-page render failed")
	}
	renderDigest := sha256.Sum256(rendered)
	evidence.renderedFinalPageSHA256 = hex.EncodeToString(renderDigest[:])
	imageValue, err := png.Decode(bytes.NewReader(rendered))
	if err != nil || imageValue.Bounds().Dx() < 600 || imageValue.Bounds().Dy() < 780 {
		return evidence, errors.New("document transfer final-page render failed")
	}
	blueR, blueG, blueB, _ := imageValue.At(170, 252).RGBA()
	orangeR, orangeG, orangeB, _ := imageValue.At(398, 252).RGBA()
	evidence.renderedGeometryPassed = blueB > blueG && blueG > blueR && orangeR > orangeG && orangeG > orangeB
	if !evidence.renderedGeometryPassed {
		return evidence, errors.New("document transfer rendered geometry changed")
	}
	return evidence, nil
}

// measurePDFValidatorHighWater runs the candidate's private bytes-only helper
// directly against the exact artifact. Its waited rusage is combined with the
// gateway's independently measured high-water mark below. The sum is a
// conservative process-tree upper bound; it does not assume the two peaks were
// simultaneous and does not mistake helper-process isolation for a sandbox.
func measurePDFValidatorHighWater(ctx context.Context, gatewayBin, artifactPath string) (int64, error) {
	artifact, err := os.Open(artifactPath)
	if err != nil {
		return 0, errors.New("document transfer validator resource measurement failed")
	}
	defer artifact.Close()
	info, err := artifact.Stat()
	if err != nil || info.Size() != obsidian.DocumentFixtureMaxBytes {
		return 0, errors.New("document transfer validator resource measurement failed")
	}
	cmd := exec.CommandContext(ctx, gatewayBin, "internal-pdf-validator", strconv.FormatInt(info.Size(), 10))
	cmd.Stdin = artifact
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return 0, errors.New("document transfer validator resource measurement failed")
	}
	usage, err := waitedUsageFromProcessState(cmd.ProcessState)
	if err != nil || usage.highWaterRSSBytes <= 0 {
		return 0, errors.New("document transfer validator resource measurement failed")
	}
	return usage.highWaterRSSBytes, nil
}

func callExactDocumentTransfer(ctx context.Context, session *sdk.ClientSession, transport *countingTransport, path string) (documentTransferCallEvidence, error) {
	beforeWire := transport.read.Load()
	beforeFrames := transport.frames.Load()
	started := time.Now()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolReadDocument, Arguments: map[string]any{"path": path}})
	latency := time.Since(started)
	afterWire := transport.read.Load()
	afterFrames := transport.frames.Load()
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		return documentTransferCallEvidence{}, errors.New("document transfer capacity call failed")
	}
	embedded, ok := result.Content[0].(*sdk.EmbeddedResource)
	if !ok || embedded.Resource == nil || !strings.HasPrefix(embedded.Resource.URI, "obsidian://read-document/") || !strings.HasSuffix(embedded.Resource.URI, ".pdf") || embedded.Resource.MIMEType != "application/pdf" {
		return documentTransferCallEvidence{}, errors.New("document transfer capacity resource identity changed")
	}
	data := embedded.Resource.Blob
	digest := sha256.Sum256(data)
	rawSHA := hex.EncodeToString(digest[:])
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil || len(structured) == 0 || len(structured) > obsidian.MaxStructuredResultBytes {
		return documentTransferCallEvidence{}, errors.New("document transfer capacity metadata exceeded its bound")
	}
	var metadata obsidian.ReadDocumentOutput
	if json.Unmarshal(structured, &metadata) != nil || !metadata.OK || metadata.RawBytes != int64(len(data)) || metadata.MIMEType != "application/pdf" || metadata.Format != "pdf" {
		return documentTransferCallEvidence{}, errors.New("document transfer capacity metadata changed")
	}
	nonceOffset := bytes.Index(data, []byte(obsidian.DocumentFixtureNonce))
	finalPageOffset := bytes.LastIndex(data, []byte("5 0 obj"))
	visualOffset := bytes.LastIndex(data, []byte("0.141176 0.419608 0.992157 rg"))
	terminal := nonceOffset >= len(data)-4096 && finalPageOffset >= len(data)-4096 && visualOffset >= len(data)-4096
	return documentTransferCallEvidence{
		rawBytes: len(data), rawSHA256: rawSHA, encodedWireBytes: afterWire - beforeWire, encodedWireFrames: afterFrames - beforeFrames,
		structuredBytes: len(structured), latency: latency, nonceOffset: nonceOffset,
		finalPageOffset: finalPageOffset, visualOffset: visualOffset, terminalEvidence: terminal,
	}, nil
}

func callExactHTTPDocumentTransfer(ctx context.Context, session *sdk.ClientSession, path string) (documentTransferCallEvidence, error) {
	started := time.Now()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolReadDocument, Arguments: map[string]any{"path": path}})
	latency := time.Since(started)
	if err != nil {
		return documentTransferCallEvidence{}, errors.New("HTTP document transfer capacity call failed")
	}
	return verifyExactHTTPDocumentResult(result, latency)
}

func verifyExactHTTPDocumentResult(result *sdk.CallToolResult, latency time.Duration) (documentTransferCallEvidence, error) {
	if result == nil || result.IsError || len(result.Content) != 1 {
		return documentTransferCallEvidence{}, errors.New("HTTP document transfer capacity call failed")
	}
	embedded, ok := result.Content[0].(*sdk.EmbeddedResource)
	if !ok || embedded.Resource == nil || !strings.HasPrefix(embedded.Resource.URI, "obsidian://read-document/") ||
		!strings.HasSuffix(embedded.Resource.URI, ".pdf") || embedded.Resource.MIMEType != "application/pdf" {
		return documentTransferCallEvidence{}, errors.New("HTTP document transfer resource identity changed")
	}
	data := embedded.Resource.Blob
	digest := sha256.Sum256(data)
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil || len(structured) == 0 || len(structured) > obsidian.MaxStructuredResultBytes {
		return documentTransferCallEvidence{}, errors.New("HTTP document transfer metadata exceeded its bound")
	}
	var metadata obsidian.ReadDocumentOutput
	if json.Unmarshal(structured, &metadata) != nil || !metadata.OK || metadata.RawBytes != int64(len(data)) ||
		metadata.MIMEType != "application/pdf" || metadata.Format != "pdf" {
		return documentTransferCallEvidence{}, errors.New("HTTP document transfer metadata changed")
	}
	return documentTransferCallEvidence{
		rawBytes: len(data), rawSHA256: hex.EncodeToString(digest[:]), structuredBytes: len(structured), latency: latency,
	}, nil
}

func callExpectedDocumentError(ctx context.Context, session *sdk.ClientSession, path, code string, forbidden ...string) error {
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolReadDocument, Arguments: map[string]any{"path": path}})
	if err != nil {
		return errors.New("HTTP document rejection did not return a structured tool error")
	}
	return verifyDocumentErrorResult(result, code, forbidden...)
}

func verifyDocumentErrorResult(result *sdk.CallToolResult, code string, forbidden ...string) error {
	if result == nil || !result.IsError || len(result.Content) != 1 {
		return errors.New("HTTP document rejection did not return a structured tool error")
	}
	textContent, ok := result.Content[0].(*sdk.TextContent)
	if !ok || textContent.Text != "Obsidian request failed; inspect the structured error." {
		return errors.New("HTTP document rejection content was not sanitized")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return errors.New("HTTP document rejection structured content was invalid")
	}
	var output obsidian.ReadDocumentOutput
	if json.Unmarshal(encoded, &output) != nil || output.OK || output.Error == nil || output.Error.Code != code || output.Error.Message == "" {
		return errors.New("HTTP document rejection code changed")
	}
	resultText := string(encoded) + textContent.Text
	for _, value := range forbidden {
		if value != "" && strings.Contains(resultText, value) {
			return errors.New("HTTP document rejection leaked private material")
		}
	}
	if strings.Contains(resultText, "native-document") || strings.Contains(resultText, "obsidian://read-document/") {
		return errors.New("HTTP document rejection exposed a native payload marker")
	}
	return nil
}

func vaultProofSnapshot(root string) (string, error) {
	records := make([]string, 0, 8)
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		if len(records) >= 32 {
			return errors.New("document transfer vault snapshot exceeded its entry bound")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		record := relative + "\x00" + info.Mode().String() + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
		switch {
		case info.Mode().IsRegular():
			if info.Size() < 0 || info.Size() > int64(obsidian.RejectedDocumentBytes) {
				return errors.New("document transfer vault snapshot file exceeded its byte bound")
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			contentHash := sha256.New()
			read, copyErr := io.Copy(contentHash, io.LimitReader(file, int64(obsidian.RejectedDocumentBytes)+1))
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || read != info.Size() {
				return errors.New("document transfer vault snapshot read failed")
			}
			record += "\x00sha256:" + hex.EncodeToString(contentHash.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			record += "\x00target:" + target
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return "", errors.New("document transfer vault snapshot failed")
	}
	sort.Strings(records)
	hash := sha256.New()
	for _, record := range records {
		_, _ = io.WriteString(hash, record+"\n")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateDocumentTelemetry(snapshot sqliteSnapshot, expectedRows int, forbidden ...string) (bool, string) {
	if snapshot.toolCallRows != expectedRows || snapshot.parsedBodyRows != snapshot.persistedRows || snapshot.persistedRows < snapshot.toolCallRows {
		return false, "row_counts"
	}
	readCalls, resolveCalls, okCalls, errorCalls := 0, 0, 0, 0
	errorCodes := make(map[string]int)
	for _, row := range snapshot.toolCallBodies {
		encoded, err := json.Marshal(row.body)
		if err != nil {
			return false, "body_encoding"
		}
		text := string(encoded)
		for _, value := range forbidden {
			if value != "" && strings.Contains(text, value) {
				return false, "private_material"
			}
		}
		if _, ok := row.body["summary"].(map[string]any); !ok {
			summaryError, _ := row.body["summary_error"].(string)
			return false, "safe_summary_" + row.tool + "_" + row.outcome + "_" + summaryError
		}
		if row.method != "tools/call" || row.body["method"] != row.method || row.body["tool"] != row.tool || row.body["outcome"] != row.outcome {
			return false, "indexed_parity"
		}
		if bodyCode, _ := row.body["error_code"].(string); bodyCode != row.errorCode {
			return false, "error_code_parity"
		}
		switch row.tool {
		case obsidian.ToolReadDocument:
			readCalls++
		case obsidian.ToolResolve:
			resolveCalls++
		default:
			return false, "tool_distribution"
		}
		switch row.outcome {
		case "ok":
			okCalls++
		case "tool_error":
			errorCalls++
			errorCodes[row.errorCode]++
		default:
			return false, "outcome_distribution"
		}
	}
	valid := readCalls == expectedRows-1 && resolveCalls == 1 && okCalls == 8 && errorCalls == 6 &&
		errorCodes["document_busy"] == 1 && errorCodes["input_too_large"] == 1 && errorCodes["unsupported_file"] == 1 &&
		errorCodes["malformed_document"] == 1 && errorCodes["path_denied"] == 1 && errorCodes["symlink_denied"] == 1
	if !valid {
		return false, "expected_distribution_r" + strconv.Itoa(readCalls) + "_z" + strconv.Itoa(resolveCalls) +
			"_o" + strconv.Itoa(okCalls) + "_e" + strconv.Itoa(errorCalls) + "_busy" + strconv.Itoa(errorCodes["document_busy"]) +
			"_large" + strconv.Itoa(errorCodes["input_too_large"]) + "_unsupported" + strconv.Itoa(errorCodes["unsupported_file"]) +
			"_malformed" + strconv.Itoa(errorCodes["malformed_document"]) + "_path" + strconv.Itoa(errorCodes["path_denied"]) +
			"_symlink" + strconv.Itoa(errorCodes["symlink_denied"])
	}
	return true, "ok"
}

func waitForHTTPReady(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return errors.New("HTTP document transfer readiness failed")
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("HTTP document transfer readiness failed")
		case <-ticker.C:
		}
	}
}

func probeExactHTTPDocumentTransfer(ctx context.Context, gatewayBin, root, path string, sampler resourceSampler) (documentTransferHTTPReport, error) {
	report := documentTransferHTTPReport{EveryCallWithinTwoSeconds: true}
	const privateSentinel = "closeout-private-sentinel"
	negativeFixtures := []struct {
		path string
		code string
	}{
		{"closeout-first-disallowed.pdf", "input_too_large"},
		{"closeout-unsupported.txt", "unsupported_file"},
		{"closeout-malformed.pdf", "malformed_document"},
		{".closeout-hidden.pdf", "path_denied"},
		{"closeout-symlink.pdf", "symlink_denied"},
	}
	for _, fixture := range negativeFixtures {
		defer os.Remove(filepath.Join(root, fixture.path))
	}
	disallowed, err := os.OpenFile(filepath.Join(root, negativeFixtures[0].path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil || disallowed.Truncate(int64(obsidian.RejectedDocumentBytes)) != nil || disallowed.Close() != nil {
		return report, errors.New("HTTP document rejection fixture setup failed")
	}
	if err := os.WriteFile(filepath.Join(root, negativeFixtures[1].path), []byte(privateSentinel), 0o600); err != nil {
		return report, errors.New("HTTP document rejection fixture setup failed")
	}
	if err := os.WriteFile(filepath.Join(root, negativeFixtures[2].path), []byte("%PDF-1.7\n"+privateSentinel+"\n%%EOF\n"), 0o600); err != nil {
		return report, errors.New("HTTP document rejection fixture setup failed")
	}
	if err := os.WriteFile(filepath.Join(root, negativeFixtures[3].path), []byte("%PDF-1.7\n"+privateSentinel+"\n%%EOF\n"), 0o600); err != nil {
		return report, errors.New("HTTP document rejection fixture setup failed")
	}
	if err := os.Symlink(path, filepath.Join(root, negativeFixtures[4].path)); err != nil {
		return report, errors.New("HTTP document rejection fixture setup failed")
	}
	vaultBefore, err := vaultProofSnapshot(root)
	if err != nil {
		return report, err
	}
	dbPath, cleanupDB, err := newPrivateSQLiteStore()
	if err != nil {
		return report, err
	}
	defer cleanupDB()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return report, errors.New("HTTP document transfer listener reservation failed")
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return report, errors.New("HTTP document transfer listener reservation failed")
	}
	cmd := exec.Command(gatewayBin, "http", "--obsidian-root", root, "--addr", address, "--telemetry-db", dbPath)
	candidateStderr := newSignalingBuffer()
	cmd.Stderr = candidateStderr
	if err := cmd.Start(); err != nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return report, errors.New("HTTP document transfer candidate start failed")
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-waited
		}
	}
	defer stop()
	baseURL := "http://" + address
	readyCtx, cancelReady := context.WithTimeout(ctx, 5*time.Second)
	err = waitForHTTPReady(readyCtx, baseURL)
	cancelReady()
	if err != nil {
		return report, err
	}
	baseTransport := &http.Transport{}
	stallingTransport := &stallingRoundTripper{base: baseTransport}
	httpClient := &http.Client{Transport: stallingTransport}
	client := sdk.NewClient(&sdk.Implementation{Name: "document-transfer-capacity-http", Version: "v1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: baseURL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return report, errors.New("HTTP document transfer candidate connection failed")
	}
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			_ = session.Close()
		}
	}()
	if report.DescriptorCount, err = requireExactToolListForSurface(ctx, session, candidateToolSurface); err != nil {
		return report, err
	}
	baseline, err := sampler.Sample(ctx, cmd.Process.Pid, true)
	if err != nil {
		return report, err
	}
	report.BaselineRSSBytes, report.BaselineFDCount = baseline.rssBytes, baseline.fdCount
	for i := 0; i < 3; i++ {
		call, callErr := callExactHTTPDocumentTransfer(ctx, session, path)
		if callErr != nil {
			return report, callErr
		}
		if call.rawBytes != obsidian.DocumentFixtureMaxBytes || call.rawSHA256 != obsidian.DocumentFixtureSHA256 {
			return report, errors.New("HTTP document transfer exact bytes changed")
		}
		report.SequentialCallCount++
		if call.latency.Microseconds() > report.MaxCallLatencyMicroseconds {
			report.MaxCallLatencyMicroseconds = call.latency.Microseconds()
		}
		report.EveryCallWithinTwoSeconds = report.EveryCallWithinTwoSeconds && call.latency < resourceCallTimeLimit
		report.RawBytes, report.RawSHA256 = call.rawBytes, call.rawSHA256
	}
	type concurrentResult struct {
		result  *sdk.CallToolResult
		err     error
		latency time.Duration
	}
	startConcurrent := make(chan struct{})
	concurrent := make(chan concurrentResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-startConcurrent
			started := time.Now()
			result, callErr := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolReadDocument, Arguments: map[string]any{"path": path}})
			concurrent <- concurrentResult{result: result, err: callErr, latency: time.Since(started)}
		}()
	}
	close(startConcurrent)
	successes, busy := 0, 0
	for i := 0; i < 2; i++ {
		outcome := <-concurrent
		if outcome.err == nil {
			if evidence, verifyErr := verifyExactHTTPDocumentResult(outcome.result, outcome.latency); verifyErr == nil && evidence.rawSHA256 == obsidian.DocumentFixtureSHA256 {
				successes++
				continue
			}
			if verifyDocumentErrorResult(outcome.result, "document_busy", privateSentinel, path) == nil {
				busy++
				report.BusyErrorSanitized = true
				continue
			}
		}
		return report, errors.New("HTTP document concurrent admission result changed")
	}
	report.ConcurrentAdmissionPassed = successes == 1 && busy == 1
	if !report.ConcurrentAdmissionPassed {
		return report, errors.New("HTTP document concurrent admission did not admit exactly one payload")
	}
	if retry, retryErr := callExactHTTPDocumentTransfer(ctx, session, path); retryErr != nil || retry.rawSHA256 != obsidian.DocumentFixtureSHA256 {
		return report, errors.New("HTTP document retry after busy failed")
	}
	report.RetryAfterBusySucceeded = true
	for _, fixture := range negativeFixtures {
		if err := callExpectedDocumentError(ctx, session, fixture.path, fixture.code, privateSentinel, fixture.path, path); err != nil {
			return report, err
		}
		report.NegativeCallCount++
	}
	report.FirstDisallowedSizeRejected = report.NegativeCallCount > 0
	report.NegativeMatrixPassed = report.NegativeCallCount == len(negativeFixtures)
	startedReading, releaseReading := stallingTransport.arm()
	stalledResult := make(chan error, 1)
	backpressureStarted := time.Now()
	go func() {
		stallCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		_, callErr := session.CallTool(stallCtx, &sdk.CallToolParams{Name: obsidian.ToolReadDocument, Arguments: map[string]any{"path": path}})
		stalledResult <- callErr
	}()
	select {
	case <-startedReading:
	case <-time.After(5 * time.Second):
		close(releaseReading)
		return report, errors.New("HTTP document stalled client did not reach response body")
	}
	select {
	case <-candidateStderr.signal:
		close(releaseReading)
		return report, errors.New("HTTP document response deadline fired before its lower bound")
	case <-time.After(documentBackpressureLowerBound):
	case <-ctx.Done():
		close(releaseReading)
		return report, errors.New("HTTP document response deadline observation canceled")
	}
	select {
	case <-candidateStderr.signal:
		report.SerializationFailureObserved = true
	case <-time.After(documentBackpressureUpperBound - documentBackpressureLowerBound):
		close(releaseReading)
		return report, errors.New("HTTP document response deadline exceeded its upper bound")
	case <-ctx.Done():
		close(releaseReading)
		return report, errors.New("HTTP document response deadline observation canceled")
	}
	close(releaseReading)
	stalledErr := <-stalledResult
	report.BackpressureElapsedMicros = time.Since(backpressureStarted).Microseconds()
	report.BackpressureDeadlinePassed = stalledErr != nil && report.BackpressureElapsedMicros >= documentBackpressureLowerBound.Microseconds() && report.BackpressureElapsedMicros < documentBackpressureUpperBound.Microseconds()
	if !report.BackpressureDeadlinePassed {
		return report, errors.New("HTTP document stalled client did not fail at the response deadline")
	}
	// A timed-out response is necessarily truncated, so that SDK session can no
	// longer parse its JSON-RPC stream. Reconnect to the same stateless server
	// and persistent audit sink to prove server-side payload/admission recovery.
	_ = session.Close()
	sessionClosed = true
	recoveryClient := sdk.NewClient(&sdk.Implementation{Name: "document-transfer-capacity-http-recovery", Version: "v1"}, nil)
	session, err = recoveryClient.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: baseURL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return report, errors.New("HTTP document reconnect after stalled client failed")
	}
	sessionClosed = false
	if retry, retryErr := callExactHTTPDocumentTransfer(ctx, session, path); retryErr != nil || retry.rawSHA256 != obsidian.DocumentFixtureSHA256 {
		return report, errors.New("HTTP document retry after stalled client failed")
	}
	report.BackpressureRetrySucceeded = true
	followup, _, isError, err := callResourceCandidate[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{"path": "."})
	report.FollowupSucceeded = err == nil && !isError && followup.OK && followup.Exists && followup.Type == "directory"
	if !report.FollowupSucceeded {
		return report, errors.New("HTTP document transfer same-session follow-up failed")
	}
	if err := waitResource(ctx, resourceStabilize30); err != nil {
		return report, err
	}
	postCall, err := sampler.Sample(ctx, cmd.Process.Pid, true)
	if err != nil {
		return report, err
	}
	report.PostCallRSSBytes, report.PostCallFDCount = postCall.rssBytes, postCall.fdCount
	report.RetainedRSSGrowthBytes = nonnegativeDelta(postCall.rssBytes, baseline.rssBytes)
	report.RetainedRSSGrowthWithinBound = report.RetainedRSSGrowthBytes <= resourceRSSGrowthLimitBytes
	// A readiness probe connection may finish closing after the SDK session is
	// established. Fewer descriptors is recovery, while any increase is a leak.
	report.AllFDsRecovered = postCall.fdCount <= baseline.fdCount
	vaultAfter, err := vaultProofSnapshot(root)
	if err != nil {
		return report, err
	}
	report.VaultUnchanged = vaultAfter == vaultBefore
	if !report.VaultUnchanged {
		return report, errors.New("HTTP document proof changed the vault fixtures")
	}
	telemetry, err := inspectSQLite(ctx, dbPath)
	if err != nil {
		return report, err
	}
	report.SQLiteToolCallRows = telemetry.toolCallRows
	const expectedToolCallRows = 14
	report.SQLiteTelemetryValidated = telemetry.toolCallRows == expectedToolCallRows && telemetry.parsedBodyRows == telemetry.persistedRows && telemetry.persistedRows >= telemetry.toolCallRows
	report.TelemetryPrivacyValidated, report.TelemetryValidationCode = validateDocumentTelemetry(telemetry, expectedToolCallRows, privateSentinel, path, obsidian.DocumentFixtureNonce, "obsidian://read-document/")
	if !report.SQLiteTelemetryValidated || !report.TelemetryPrivacyValidated {
		return report, errors.New("HTTP document persisted telemetry proof failed")
	}
	if err := session.Close(); err != nil {
		return report, errors.New("HTTP document transfer session close failed")
	}
	sessionClosed = true
	stopped = true
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return report, errors.New("HTTP document transfer candidate stop failed")
	}
	select {
	case err := <-waited:
		if err != nil {
			return report, errors.New("HTTP document transfer candidate stop failed")
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		return report, errors.New("HTTP document transfer candidate stop timed out")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-waited
		return report, errors.New("HTTP document transfer candidate stop timed out")
	}
	baseTransport.CloseIdleConnections()
	report.SerializationFailureObserved = report.SerializationFailureObserved && candidateStderr.containsSignal()
	if !report.SerializationFailureObserved {
		return report, errors.New("HTTP document serialization failure was not retained operationally")
	}
	usage, err := waitedUsageFromProcessState(cmd.ProcessState)
	if err != nil {
		return report, err
	}
	report.HighWaterRSSBytes = usage.highWaterRSSBytes
	report.HighWaterRSSDeltaBytes = nonnegativeDelta(usage.highWaterRSSBytes, baseline.rssBytes)
	report.HighWaterWithinBound = report.HighWaterRSSDeltaBytes <= resourceRSSLimitBytes
	return report, nil
}

func documentTransferHTTPReportPasses(report documentTransferHTTPReport) bool {
	return report.DescriptorCount == candidateDescriptorCount && report.SequentialCallCount == 3 &&
		report.RawBytes == obsidian.DocumentFixtureMaxBytes && report.RawSHA256 == obsidian.DocumentFixtureSHA256 &&
		report.MaxCallLatencyMicroseconds > 0 && report.MaxCallLatencyMicroseconds < resourceCallTimeLimit.Microseconds() &&
		report.EveryCallWithinTwoSeconds && report.FollowupSucceeded &&
		report.BaselineRSSBytes > 0 && report.PostCallRSSBytes > 0 &&
		report.RetainedRSSGrowthBytes == nonnegativeDelta(report.PostCallRSSBytes, report.BaselineRSSBytes) &&
		report.RetainedRSSGrowthBytes <= resourceRSSGrowthLimitBytes && report.RetainedRSSGrowthWithinBound &&
		report.HighWaterRSSBytes >= report.BaselineRSSBytes && report.HighWaterRSSDeltaBytes == nonnegativeDelta(report.HighWaterRSSBytes, report.BaselineRSSBytes) &&
		report.HighWaterRSSDeltaBytes <= resourceRSSLimitBytes && report.HighWaterWithinBound &&
		report.BaselineFDCount > 0 && report.PostCallFDCount > 0 && report.PostCallFDCount <= report.BaselineFDCount && report.AllFDsRecovered &&
		report.FirstDisallowedSizeRejected && report.NegativeCallCount == 5 && report.NegativeMatrixPassed && report.VaultUnchanged &&
		report.ConcurrentAdmissionPassed && report.BusyErrorSanitized && report.RetryAfterBusySucceeded &&
		report.BackpressureDeadlinePassed && report.BackpressureElapsedMicros >= documentBackpressureLowerBound.Microseconds() &&
		report.BackpressureElapsedMicros < documentBackpressureUpperBound.Microseconds() && report.BackpressureRetrySucceeded &&
		report.SerializationFailureObserved && report.SQLiteToolCallRows == 14 && report.SQLiteTelemetryValidated && report.TelemetryPrivacyValidated && report.TelemetryValidationCode == "ok"
}

func probeDocumentTransferCapacity(ctx context.Context, gatewayBin, artifactPath string, provenance candidateProvenance, sampler resourceSampler) (documentTransferCapacityReport, error) {
	report := documentTransferCapacityReport{
		ReportKind: "document_transfer_capacity", ReportSchema: documentTransferCapacitySchema, SchemaVersion: documentTransferCapacityVersion,
		CandidateCommit: provenance.Commit, CandidateSHA256: provenance.CandidateSHA256, DependencySHA256: provenance.DependencySHA256,
	}
	var err error
	if artifactPath == "" {
		return report, errors.New("document transfer artifact path is required")
	}
	fixture, err := obsidian.GenerateDocumentCapacityFixture()
	if err != nil || len(fixture) != obsidian.DocumentFixtureMaxBytes {
		return report, errors.New("document transfer fixture generation failed")
	}
	if err := os.WriteFile(artifactPath, fixture, 0o600); err != nil {
		return report, errors.New("document transfer artifact write failed")
	}
	artifact, err := validateDocumentTransferArtifact(ctx, artifactPath, obsidian.DocumentFixtureSHA256)
	if err != nil {
		return report, err
	}
	report.ArtifactSHA256 = artifact.artifactSHA256
	report.PDFValidatorAccepted = artifact.pdfValidatorAccepted
	report.TerminalTextExtractionPassed = artifact.terminalTextExtractionPassed
	report.RenderedFinalPageSHA256 = artifact.renderedFinalPageSHA256
	report.RenderedGeometryPassed = artifact.renderedGeometryPassed
	report.FixtureLeakageCheckPassed = artifact.fixtureLeakageCheckPassed
	report.ValidatorHighWaterRSSBytes, err = measurePDFValidatorHighWater(ctx, gatewayBin, artifactPath)
	if err != nil {
		return report, err
	}
	fixture = nil
	if report.CandidateRuntime, err = inspectCandidateRuntime(gatewayBin); err != nil {
		return report, err
	}
	if report.Machine, err = inspectMachineProfile(); err != nil {
		return report, err
	}
	candidate, transport, err := connectCountingResourceCandidate(ctx, gatewayBin, filepath.Dir(artifactPath))
	if err != nil {
		return report, err
	}
	defer candidate.closeDiscard()
	if report.DescriptorCount, err = requireExactToolListForSurface(ctx, candidate.process.session, candidateToolSurface); err != nil {
		return report, err
	}
	started := time.Now()
	baseline, err := observeDocumentTransferCheckpoint(ctx, started, candidate, sampler)
	if err != nil {
		return report, err
	}
	report.Baseline = baseline
	activityBefore, err := candidate.control.snapshot(ctx, resourceControlTime)
	if err != nil {
		return report, err
	}
	var call documentTransferCallEvidence
	for i := 0; i < 3; i++ {
		current, callErr := callExactDocumentTransfer(ctx, candidate.process.session, transport, filepath.Base(artifactPath))
		if callErr != nil {
			return report, callErr
		}
		if i == 0 {
			call = current
		} else if current.rawBytes != call.rawBytes || current.rawSHA256 != call.rawSHA256 || current.encodedWireFrames != 1 || !current.terminalEvidence {
			return report, errors.New("document transfer sequential call changed")
		}
		if current.latency > call.latency {
			call.latency = current.latency
		}
		report.SequentialCallCount++
	}
	report.RawBytes, report.RawSHA256, report.EncodedWireBytes = call.rawBytes, call.rawSHA256, call.encodedWireBytes
	report.EncodedWireFrames = call.encodedWireFrames
	report.StructuredResultBytes, report.CallLatencyMicroseconds = call.structuredBytes, call.latency.Microseconds()
	report.CallWithinTwoSeconds = call.latency < resourceCallTimeLimit
	report.NonceOffset, report.FinalPageObjectOffset, report.VisualEvidenceOffset = call.nonceOffset, call.finalPageOffset, call.visualOffset
	report.TerminalEvidenceWithin4096Bytes = call.terminalEvidence
	for _, delay := range []time.Duration{0, 5 * time.Second, 25 * time.Second} {
		if delay > 0 {
			if err := waitResource(ctx, delay); err != nil {
				return report, err
			}
		}
		checkpoint, err := observeDocumentTransferCheckpoint(ctx, started, candidate, sampler)
		if err != nil {
			return report, err
		}
		report.PostCall = append(report.PostCall, checkpoint)
	}
	report.GCAcknowledgementCount = 1 + len(report.PostCall)
	activityAfter, err := candidate.control.snapshot(ctx, resourceControlTime)
	if err != nil {
		return report, err
	}
	report.ActivityQuiescent = activityBefore.active == 0 && activityAfter.active == 0 && activityBefore.grepActive == 0 && activityAfter.grepActive == 0 && activityAfter.grepInFlight == 0
	followup, _, isError, err := callResourceCandidate[obsidian.ResolveOutput](ctx, candidate.process.session, obsidian.ToolResolve, map[string]any{"path": "."})
	report.FollowupSucceeded = err == nil && !isError && followup.OK && followup.Exists && followup.Type == "directory"
	if !report.FollowupSucceeded {
		return report, errors.New("document transfer same-session follow-up failed")
	}
	report.Idle, err = observeResourceIdleExpected(ctx, candidate.process.session, candidate.process.command.Process.Pid, report.DescriptorCount, report.Baseline.FDCount, candidate.dbPath, defaultResourceProbeOptions(), sampler, candidate.control, 4)
	if err != nil {
		return report, err
	}
	usage, err := candidate.closeWithUsage()
	if err != nil {
		return report, err
	}
	report.HighWaterRSSBytes = usage.highWaterRSSBytes
	report.HighWaterRSSDeltaBytes = nonnegativeDelta(report.HighWaterRSSBytes, report.Baseline.RSSBytes)
	report.HighWaterWithinBound = report.HighWaterRSSDeltaBytes <= resourceRSSLimitBytes
	for _, checkpoint := range report.PostCall {
		report.RetainedHeapAllocGrowthBytes = maxUint64(report.RetainedHeapAllocGrowthBytes, nonnegativeUint64Delta(checkpoint.Memory.HeapAllocBytes, report.Baseline.Memory.HeapAllocBytes))
		report.RetainedRSSWindowGrowthBytes = maxInt64(report.RetainedRSSWindowGrowthBytes, nonnegativeDelta(checkpoint.RSSBytes, report.Baseline.RSSBytes))
		report.AllFDsRecovered = report.AllFDsRecovered || checkpoint.FDCount == report.Baseline.FDCount
	}
	report.AllFDsRecovered = len(report.PostCall) == 3 && report.PostCall[0].FDCount == report.Baseline.FDCount && report.PostCall[1].FDCount == report.Baseline.FDCount && report.PostCall[2].FDCount == report.Baseline.FDCount
	report.RetainedHeapAllocGrowthWithinBound = report.RetainedHeapAllocGrowthBytes <= resourceHeapAllocGrowthLimitBytes
	report.RetainedRSSWindowGrowthWithinBound = report.RetainedRSSWindowGrowthBytes <= resourceRSSGrowthLimitBytes
	report.HTTP, err = probeExactHTTPDocumentTransfer(ctx, gatewayBin, filepath.Dir(artifactPath), filepath.Base(artifactPath), sampler)
	if err != nil {
		return report, err
	}
	report.FirstDisallowedSizeRejected = report.HTTP.FirstDisallowedSizeRejected
	report.AggregateHighWaterUpperBoundBytes = maxInt64(report.HighWaterRSSBytes, report.HTTP.HighWaterRSSBytes) + report.ValidatorHighWaterRSSBytes
	report.AggregateHighWaterWithinBound = report.AggregateHighWaterUpperBoundBytes <= documentTransferAggregateRSSLimitBytes
	report.Passed = documentTransferCapacityReportPasses(report)
	if !report.Passed {
		return report, errors.New("document transfer capacity gate failed")
	}
	return report, nil
}

func documentTransferCapacityReportPasses(report documentTransferCapacityReport) bool {
	return reportSchemaTuplePasses(report.ReportKind, report.ReportSchema, report.SchemaVersion) &&
		candidateRuntimeProfilePasses(report.CandidateRuntime) && machineProfilePasses(report.Machine) &&
		report.DescriptorCount == candidateDescriptorCount && report.RawBytes == obsidian.DocumentFixtureMaxBytes &&
		report.RawSHA256 == obsidian.DocumentFixtureSHA256 && report.EncodedWireBytes > int64(report.RawBytes) && report.EncodedWireFrames == 1 &&
		report.StructuredResultBytes > 0 && report.StructuredResultBytes <= obsidian.MaxStructuredResultBytes &&
		documentTransferStdioEvidencePasses(report) && report.ArtifactSHA256 == report.RawSHA256 && report.PDFValidatorAccepted &&
		report.TerminalTextExtractionPassed && validDigest(report.RenderedFinalPageSHA256) && report.RenderedGeometryPassed && report.FixtureLeakageCheckPassed &&
		report.FirstDisallowedSizeRejected && report.ValidatorHighWaterRSSBytes > 0 &&
		report.AggregateHighWaterUpperBoundBytes == maxInt64(report.HighWaterRSSBytes, report.HTTP.HighWaterRSSBytes)+report.ValidatorHighWaterRSSBytes &&
		report.AggregateHighWaterUpperBoundBytes > report.ValidatorHighWaterRSSBytes &&
		report.AggregateHighWaterUpperBoundBytes <= documentTransferAggregateRSSLimitBytes && report.AggregateHighWaterWithinBound &&
		report.RetainedHeapAllocGrowthWithinBound && report.RetainedRSSWindowGrowthWithinBound &&
		report.AllFDsRecovered && report.ActivityQuiescent && report.FollowupSucceeded &&
		idleResourceReportPassesExpected(report.Idle, report.Baseline.FDCount, report.DescriptorCount, candidateToolSurface, 4) &&
		documentTransferHTTPReportPasses(report.HTTP)
}

func documentTransferStdioEvidencePasses(report documentTransferCapacityReport) bool {
	if report.SequentialCallCount != 3 || report.CallLatencyMicroseconds <= 0 ||
		report.CallLatencyMicroseconds >= resourceCallTimeLimit.Microseconds() || !report.CallWithinTwoSeconds ||
		!report.TerminalEvidenceWithin4096Bytes || report.RawBytes < 4096 ||
		report.NonceOffset < report.RawBytes-4096 || report.NonceOffset >= report.RawBytes ||
		report.FinalPageObjectOffset < report.RawBytes-4096 || report.FinalPageObjectOffset >= report.RawBytes ||
		report.VisualEvidenceOffset < report.RawBytes-4096 || report.VisualEvidenceOffset >= report.RawBytes ||
		report.Baseline.RSSBytes <= 0 || report.Baseline.FDCount <= 0 || report.Baseline.Memory.HeapAllocBytes == 0 ||
		len(report.PostCall) != 3 || report.GCAcknowledgementCount != 1+len(report.PostCall) ||
		report.HighWaterRSSBytes < report.Baseline.RSSBytes ||
		report.HighWaterRSSDeltaBytes != nonnegativeDelta(report.HighWaterRSSBytes, report.Baseline.RSSBytes) ||
		report.HighWaterRSSDeltaBytes > resourceRSSLimitBytes || !report.HighWaterWithinBound {
		return false
	}
	var heapGrowth uint64
	var rssGrowth int64
	lastElapsed := report.Baseline.ElapsedMicroseconds
	for _, checkpoint := range report.PostCall {
		if checkpoint.ElapsedMicroseconds < lastElapsed || checkpoint.RSSBytes <= 0 || checkpoint.FDCount != report.Baseline.FDCount ||
			checkpoint.Memory.HeapAllocBytes == 0 {
			return false
		}
		lastElapsed = checkpoint.ElapsedMicroseconds
		heapGrowth = maxUint64(heapGrowth, nonnegativeUint64Delta(checkpoint.Memory.HeapAllocBytes, report.Baseline.Memory.HeapAllocBytes))
		rssGrowth = maxInt64(rssGrowth, nonnegativeDelta(checkpoint.RSSBytes, report.Baseline.RSSBytes))
	}
	return report.RetainedHeapAllocGrowthBytes == heapGrowth && heapGrowth <= resourceHeapAllocGrowthLimitBytes &&
		report.RetainedHeapAllocGrowthWithinBound && report.RetainedRSSWindowGrowthBytes == rssGrowth &&
		rssGrowth <= resourceRSSGrowthLimitBytes && report.RetainedRSSWindowGrowthWithinBound && report.AllFDsRecovered
}
