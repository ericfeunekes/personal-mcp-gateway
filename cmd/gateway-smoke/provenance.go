package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	maxCandidateBytes = int64(256 << 20)
	maxDependencyFile = int64(8 << 20)
	maxReportFile     = int64(1 << 20)
	provenanceTimeout = 5 * time.Second
)

const (
	reportKindFunctional  = "functional"
	reportKindPerformance = "performance"
	reportKindResource    = "resource"
)

type candidateProvenance struct {
	Commit           string
	CandidateSHA256  string
	DependencySHA256 string
}

type reportEnvelope struct {
	ReportKind       string `json:"report_kind"`
	ReportSchema     string `json:"report_schema"`
	SchemaVersion    int    `json:"schema_version"`
	Passed           bool   `json:"passed"`
	CandidateCommit  string `json:"candidate_commit"`
	CandidateSHA256  string `json:"candidate_sha256"`
	DependencySHA256 string `json:"dependency_sha256"`
}

func verifyCandidateProvenance(repoRoot, candidatePath string, expected candidateProvenance) (candidateProvenance, error) {
	if repoRoot == "" || candidatePath == "" || !validGitOID(expected.Commit) ||
		!validDigest(expected.CandidateSHA256) || !validDigest(expected.DependencySHA256) {
		return candidateProvenance{}, errors.New("candidate provenance is invalid")
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return candidateProvenance{}, errors.New("candidate provenance validation failed")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return candidateProvenance{}, errors.New("candidate provenance validation failed")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return candidateProvenance{}, errors.New("candidate provenance validation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), provenanceTimeout)
	defer cancel()
	head, err := boundedGitOutput(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil || head != expected.Commit {
		return candidateProvenance{}, errors.New("candidate commit does not match repository")
	}
	status, err := boundedGitOutput(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return candidateProvenance{}, errors.New("candidate repository is not clean")
	}
	candidateHash, err := hashRegularBounded(candidatePath, maxCandidateBytes)
	if err != nil || candidateHash != expected.CandidateSHA256 {
		return candidateProvenance{}, errors.New("candidate digest does not match")
	}
	dependencyHash, err := canonicalDependencySHA256(root)
	if err != nil || dependencyHash != expected.DependencySHA256 {
		return candidateProvenance{}, errors.New("dependency digest does not match")
	}
	return expected, nil
}

func canonicalDependencySHA256(repoRoot string) (string, error) {
	goMod, err := hashRegularBounded(filepath.Join(repoRoot, "go.mod"), maxDependencyFile)
	if err != nil {
		return "", err
	}
	goSum, err := hashRegularBounded(filepath.Join(repoRoot, "go.sum"), maxDependencyFile)
	if err != nil {
		return "", err
	}
	record := "go.mod=" + goMod + "\ngo.sum=" + goSum + "\n"
	digest := sha256.Sum256([]byte(record))
	return hex.EncodeToString(digest[:]), nil
}

func hashRegularBounded(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid regular file")
	}
	openInfo, err := file.Stat()
	if err != nil || !openInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openInfo) || openInfo.Size() < 0 || openInfo.Size() > maxBytes {
		return "", errors.New("invalid regular file")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil || written > maxBytes {
		return "", errors.New("regular file exceeds limit")
	}
	finalInfo, err := file.Stat()
	if err != nil || finalInfo.Size() != openInfo.Size() || !finalInfo.ModTime().Equal(openInfo.ModTime()) {
		return "", errors.New("regular file changed during validation")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func boundedGitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", repoRoot}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	var stdout, stderr boundedProvenanceBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil || stdout.truncated || stderr.truncated {
		return "", errors.New("git verification failed")
	}
	return strings.TrimSpace(stdout.String()), nil
}

type boundedProvenanceBuffer struct {
	data      []byte
	truncated bool
}

func (b *boundedProvenanceBuffer) Write(data []byte) (int, error) {
	const limit = 64 << 10
	original := len(data)
	remaining := limit - len(b.data)
	if remaining <= 0 {
		b.truncated = b.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, data...)
	return original, nil
}

func (b *boundedProvenanceBuffer) String() string { return string(b.data) }

func validGitOID(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validLowerHex(value)
}

func validDigest(value string) bool { return len(value) == 64 && validLowerHex(value) }

func validLowerHex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validateReportSet(paths []string, expected candidateProvenance) error {
	if len(paths) != 3 {
		return errors.New("exactly three candidate reports are required")
	}
	seen := make(map[string]bool, 3)
	for _, path := range paths {
		report, data, err := readReport(path)
		if err != nil {
			return errors.New("candidate report set is invalid")
		}
		if !reportSchemaTuplePasses(report.ReportKind, report.ReportSchema, report.SchemaVersion) || !report.Passed ||
			report.CandidateCommit != expected.Commit ||
			report.CandidateSHA256 != expected.CandidateSHA256 ||
			report.DependencySHA256 != expected.DependencySHA256 {
			return errors.New("candidate report set does not match provenance")
		}
		if err := validateReportProof(data, report.ReportKind); err != nil {
			return errors.New("candidate report set is invalid")
		}
		if seen[report.ReportKind] {
			return errors.New("candidate report set is invalid")
		}
		seen[report.ReportKind] = true
	}
	if !seen[reportKindFunctional] || !seen[reportKindPerformance] || !seen[reportKindResource] {
		return errors.New("candidate report set is incomplete")
	}
	return nil
}

func readReport(path string) (reportEnvelope, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	defer file.Close()
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Mode().Perm()&0o077 != 0 {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	openInfo, err := file.Stat()
	if err != nil || !openInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openInfo) || openInfo.Size() < 1 || openInfo.Size() > maxReportFile {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReportFile+1))
	if err != nil || int64(len(data)) != openInfo.Size() || int64(len(data)) > maxReportFile {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	finalInfo, err := file.Stat()
	if err != nil || finalInfo.Size() != openInfo.Size() || !finalInfo.ModTime().Equal(openInfo.ModTime()) {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	if err := validateUniqueJSONKeys(data); err != nil {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var report reportEnvelope
	if err := decoder.Decode(&report); err != nil {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return reportEnvelope{}, nil, errors.New("invalid candidate report")
	}
	return report, data, nil
}

func validateReportProof(data []byte, kind string) error {
	switch kind {
	case reportKindFunctional:
		var report smokeReport
		if err := decodeCompleteReport(data, &report); err != nil || !functionalReportPasses(report) {
			return errors.New("functional report proof is invalid")
		}
	case reportKindPerformance:
		var report performanceReport
		if err := decodeCompleteReport(data, &report); err != nil || !performanceReportPasses(report) {
			return errors.New("performance report proof is invalid")
		}
	case reportKindResource:
		var report resourceReport
		if err := decodeCompleteReport(data, &report); err != nil || !report.Passed || !resourceReportPasses(report, resourceColdProcesses) {
			return errors.New("resource report proof is invalid")
		}
	default:
		return errors.New("report kind is invalid")
	}
	return nil
}

func decodeCompleteReport(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("candidate report has trailing JSON")
	}
	typeOf := reflect.TypeOf(target)
	if typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return errors.New("candidate report schema is invalid")
	}
	return requireCompleteJSONShape(data, typeOf.Elem())
}

func requireCompleteJSONShape(data []byte, expected reflect.Type) error {
	if expected.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		return requireCompleteJSONShape(data, expected.Elem())
	}
	switch expected.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return errors.New("candidate report object is invalid")
		}
		for index := 0; index < expected.NumField(); index++ {
			field := expected.Field(index)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			raw, ok := object[name]
			if !ok {
				return errors.New("candidate report field is missing")
			}
			if err := requireCompleteJSONShape(raw, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil || values == nil {
			return errors.New("candidate report array is invalid")
		}
		for _, raw := range values {
			if err := requireCompleteJSONShape(raw, expected.Elem()); err != nil {
				return err
			}
		}
	default:
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return errors.New("candidate report value is missing")
		}
	}
	return nil
}

func validateUniqueJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("candidate report has trailing JSON")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("candidate report has duplicate field")
			}
			seen[key] = true
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("candidate report object is invalid")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("candidate report array is invalid")
		}
	default:
		return errors.New("candidate report JSON is invalid")
	}
	return nil
}

func functionalReportPasses(report smokeReport) bool {
	return report.Passed && functionalBehaviorPasses(report) && functionalReportEvidencePasses(report)
}

func performanceReportPasses(report performanceReport) bool {
	return report.Passed && performanceReportEvidencePasses(report)
}

func performanceMetricsPass(metrics performanceMetrics, samples int) bool {
	return performanceMetricsWithin(metrics, samples, performanceP95LimitUS)
}

func reportSchemaTuplePasses(kind, schema string, version int) bool {
	switch kind {
	case reportKindFunctional:
		return schema == functionalReportSchema && version == smokeReportVersion
	case reportKindPerformance:
		return schema == performanceReportSchema && version == smokeReportVersion
	case reportKindResource:
		return schema == resourceReportSchema && version == resourceReportVersion
	default:
		return false
	}
}

func candidateReportStratifiedMetricsPass(metrics performanceMetrics, entryCount int) bool {
	return performanceMetricsPass(metrics, stratifiedSamples) &&
		metrics.MaxFilesScanned == uint64(entryCount) && metrics.MaxBytesScanned == 0
}

func continuedMetricsPass(metrics *performanceMetrics, entryCount int, expected bool) bool {
	if !expected {
		return metrics == nil
	}
	return metrics != nil && candidateReportStratifiedMetricsPass(*metrics, entryCount)
}

func sqliteProofPasses(proof sqliteTelemetryProof, expectedMeasured int) bool {
	return proof.Validated && proof.ExpectedMeasuredToolCallRows == expectedMeasured &&
		proof.MeasuredToolCallRows == expectedMeasured && proof.TotalToolCallRows == proof.SetupToolCallRows+expectedMeasured &&
		proof.PersistedRows >= proof.TotalToolCallRows && proof.ParsedBodyRows == proof.PersistedRows
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
