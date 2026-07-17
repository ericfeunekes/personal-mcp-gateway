package obsidian

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"

	"personal-mcp-gateway/internal/fsx"
)

type grepMatchEvidence struct {
	Column          int
	Occurrences     int
	Text            string
	TextTruncated   bool
	TextStartColumn int
	TextEndColumn   int
	LineBytes       int64
}

func (g *grepRun) matchEvidence(line grepLine) (grepMatchEvidence, bool) {
	if line.matchKnown {
		if !line.matched {
			return grepMatchEvidence{}, false
		}
		return grepMatchEvidence{
			Column:          line.matchColumn,
			Occurrences:     line.occurrences,
			Text:            line.text,
			TextTruncated:   line.textTruncated,
			TextStartColumn: line.textStartColumn,
			TextEndColumn:   line.textEndColumn,
			LineBytes:       line.lineBytes,
		}, true
	}
	data := lineBytes(line)
	index := g.re.FindIndex(data)
	if len(index) == 0 {
		return grepMatchEvidence{}, false
	}
	evidence := grepMatchEvidence{
		Column:      utf8.RuneCount(data[:index[0]]) + 1,
		Occurrences: 1,
		Text:        string(data),
	}
	if !g.query.Regex {
		evidence.Occurrences = countLiteralOccurrences(g.re, data)
		if len(data) > grepEvidenceTextBytes {
			evidence.Text, evidence.TextStartColumn, evidence.TextEndColumn = grepExcerpt(data, index[0], index[1])
			evidence.TextTruncated = true
			evidence.LineBytes = int64(len(data))
		}
	}
	return evidence, true
}

func grepContextEvidence(line grepLine) GrepContextLine {
	if line.textTruncated {
		text := line.text
		endColumn := line.textEndColumn
		if line.contextText != "" {
			text = line.contextText
			endColumn = line.contextEndColumn
		}
		return GrepContextLine{
			Line:            line.number,
			Text:            text,
			TextTruncated:   true,
			TextStartColumn: 1,
			TextEndColumn:   endColumn,
			LineBytes:       line.lineBytes,
		}
	}
	data := lineBytes(line)
	if len(data) <= grepEvidenceTextBytes {
		return GrepContextLine{Line: line.number, Text: string(data)}
	}
	text, start, end := grepPrefixExcerpt(data)
	return GrepContextLine{
		Line:            line.number,
		Text:            text,
		TextTruncated:   true,
		TextStartColumn: start,
		TextEndColumn:   end,
		LineBytes:       int64(len(data)),
	}
}

func lineBytes(line grepLine) []byte {
	if line.large != nil {
		return line.large
	}
	return []byte(line.text)
}

func countLiteralOccurrences(re *regexp.Regexp, data []byte) int {
	count := 0
	for len(data) > 0 {
		index := re.FindIndex(data)
		if len(index) == 0 {
			break
		}
		count++
		data = data[index[1]:]
	}
	return count
}

func grepExcerpt(data []byte, matchStart, matchEnd int) (string, int, int) {
	start := max(0, matchStart-grepEvidenceTextBytes/4)
	start = nextUTF8Start(data, start)
	end := min(len(data), start+grepEvidenceTextBytes)
	if matchEnd > end {
		start = nextUTF8Start(data, matchStart)
		end = min(len(data), start+grepEvidenceTextBytes)
	}
	end = previousUTF8End(data, start, end)
	startColumn := utf8.RuneCount(data[:start]) + 1
	text := string(data[start:end])
	return text, startColumn, startColumn + utf8.RuneCountInString(text) - 1
}

func grepPrefixExcerpt(data []byte) (string, int, int) {
	end := previousUTF8End(data, 0, min(len(data), grepEvidenceTextBytes))
	text := string(data[:end])
	return text, 1, utf8.RuneCountInString(text)
}

func nextUTF8Start(data []byte, offset int) int {
	for offset < len(data) && !utf8.RuneStart(data[offset]) {
		offset++
	}
	return offset
}

func previousUTF8End(data []byte, start, end int) int {
	for end > start && !utf8.Valid(data[start:end]) {
		end--
	}
	return end
}

type oversizedLiteralScanner struct {
	re              *regexp.Regexp
	overlap         int
	tail            []byte
	totalBytes      int64
	totalRunes      int
	lastMatchEnd    int64
	matched         bool
	matchColumn     int
	occurrences     int
	prefix          []byte
	excerpt         []byte
	excerptStart    int64
	excerptStartCol int
	decodeCarry     []byte
	rawHold         []byte
	invalid         bool
}

func newOversizedLiteralScanner(re *regexp.Regexp) *oversizedLiteralScanner {
	return &oversizedLiteralScanner{
		re:      re,
		overlap: grepEvidenceTextBytes/2 + 4*MaxGrepPatternBytes + utf8.UTFMax,
		prefix:  make([]byte, 0, grepEvidenceTextBytes),
	}
}

func (s *oversizedLiteralScanner) writeRaw(data []byte) {
	if len(data) == 0 {
		return
	}
	combined := make([]byte, 0, len(s.rawHold)+len(data))
	combined = append(combined, s.rawHold...)
	combined = append(combined, data...)
	if len(combined) == 1 {
		s.rawHold = append(s.rawHold[:0], combined[0])
		return
	}
	s.feedUTF8(combined[:len(combined)-1])
	s.rawHold = append(s.rawHold[:0], combined[len(combined)-1])
}

func (s *oversizedLiteralScanner) finish(newline bool) {
	if len(s.rawHold) > 0 && !(newline && s.rawHold[0] == '\r') {
		s.feedUTF8(s.rawHold)
	}
	s.rawHold = nil
	if len(s.decodeCarry) > 0 {
		s.invalid = true
	}
}

func (s *oversizedLiteralScanner) feedUTF8(data []byte) {
	buffer := make([]byte, 0, len(s.decodeCarry)+len(data))
	buffer = append(buffer, s.decodeCarry...)
	buffer = append(buffer, data...)
	i := 0
	for i < len(buffer) {
		if !utf8.FullRune(buffer[i:]) {
			break
		}
		r, size := utf8.DecodeRune(buffer[i:])
		if r == utf8.RuneError && size == 1 {
			s.invalid = true
		}
		i += size
	}
	if i > 0 {
		s.processValid(buffer[:i])
	}
	s.decodeCarry = append(s.decodeCarry[:0], buffer[i:]...)
}

func (s *oversizedLiteralScanner) processValid(data []byte) {
	if len(data) == 0 {
		return
	}
	previousBytes := s.totalBytes
	if len(s.prefix) < grepEvidenceTextBytes {
		take := min(len(data), grepEvidenceTextBytes-len(s.prefix))
		s.prefix = append(s.prefix, data[:take]...)
	}
	window := make([]byte, 0, len(s.tail)+len(data))
	window = append(window, s.tail...)
	window = append(window, data...)
	windowStart := previousBytes - int64(len(s.tail))
	windowStartColumn := s.totalRunes - utf8.RuneCount(s.tail) + 1
	searchStart := max(0, int(s.lastMatchEnd-windowStart))
	for _, index := range s.re.FindAllIndex(window[searchStart:], -1) {
		index[0] += searchStart
		index[1] += searchStart
		absoluteStart := windowStart + int64(index[0])
		absoluteEnd := windowStart + int64(index[1])
		if absoluteEnd <= previousBytes || absoluteStart < s.lastMatchEnd {
			continue
		}
		s.occurrences++
		s.lastMatchEnd = absoluteEnd
		if !s.matched {
			s.matched = true
			s.matchColumn = windowStartColumn + utf8.RuneCount(window[:index[0]])
			start := max(0, index[0]-grepEvidenceTextBytes/4)
			start = nextUTF8Start(window, start)
			end := min(len(window), start+grepEvidenceTextBytes)
			if index[1] > end {
				start = nextUTF8Start(window, index[0])
				end = min(len(window), start+grepEvidenceTextBytes)
			}
			end = previousUTF8End(window, start, end)
			s.excerpt = append(s.excerpt, window[start:end]...)
			s.excerptStart = windowStart + int64(start)
			s.excerptStartCol = windowStartColumn + utf8.RuneCount(window[:start])
		}
	}
	if s.matched && len(s.excerpt) < grepEvidenceTextBytes {
		nextAbsolute := s.excerptStart + int64(len(s.excerpt))
		dataStart := previousBytes
		if nextAbsolute >= dataStart && nextAbsolute < dataStart+int64(len(data)) {
			start := int(nextAbsolute - dataStart)
			take := min(len(data)-start, grepEvidenceTextBytes-len(s.excerpt))
			s.excerpt = append(s.excerpt, data[start:start+take]...)
		}
	}
	s.totalBytes += int64(len(data))
	s.totalRunes += utf8.RuneCount(data)
	keep := min(len(window), s.overlap)
	start := nextUTF8Start(window, len(window)-keep)
	s.tail = append(s.tail[:0], window[start:]...)
}

func (s *oversizedLiteralScanner) line(number int, start, end int64) (grepLine, error) {
	if s.invalid {
		return grepLine{}, errInvalidUTF8
	}
	text := s.prefix
	startColumn := 1
	if s.matched {
		text = s.excerpt
		startColumn = s.excerptStartCol
	}
	textEnd := previousUTF8End(text, 0, len(text))
	text = text[:textEnd]
	contextEnd := previousUTF8End(s.prefix, 0, len(s.prefix))
	contextText := s.prefix[:contextEnd]
	return grepLine{
		number:           number,
		start:            start,
		end:              end,
		text:             string(text),
		textTruncated:    true,
		textStartColumn:  startColumn,
		textEndColumn:    startColumn + utf8.RuneCount(text) - 1,
		contextText:      string(contextText),
		contextEndColumn: utf8.RuneCount(contextText),
		lineBytes:        s.totalBytes,
		matchKnown:       true,
		matched:          s.matched,
		matchColumn:      s.matchColumn,
		occurrences:      s.occurrences,
	}, nil
}

func (r *grepLineReader) finishOversizedLiteral(ctx context.Context, start int64, lineNumber int, line *grepLineBuffer, terminated bool) (grepLine, error) {
	scanner := newOversizedLiteralScanner(r.oversizedLiteral)
	initial := line.bytes()
	if terminated {
		initial = initial[:len(initial)-1]
	}
	scanner.writeRaw(initial)
	line.mapped.release()
	if terminated {
		scanner.finish(true)
		r.line++
		return scanner.line(lineNumber, start, r.offset)
	}
	for {
		if r.pendingPos < len(r.pending) {
			fragment := r.pending[r.pendingPos:]
			if newline := bytes.IndexByte(fragment, '\n'); newline >= 0 {
				scanner.writeRaw(fragment[:newline])
				r.pendingPos += newline + 1
				r.offset += int64(newline + 1)
				scanner.finish(true)
				r.line++
				return scanner.line(lineNumber, start, r.offset)
			}
			scanner.writeRaw(fragment)
			r.offset += int64(len(fragment))
			r.pendingPos = len(r.pending)
		}
		if r.offset >= r.size {
			scanner.finish(false)
			r.line++
			return scanner.line(lineNumber, start, r.offset)
		}
		if r.remaining <= 0 {
			return grepLine{}, errLineBudget
		}
		readSize := min(int64(32*1024), r.remaining, r.size-r.offset)
		buffer := make([]byte, int(readSize))
		n, err := r.file.Read(ctx, buffer)
		if n > 0 {
			r.remaining -= int64(n)
			r.newBytes += int64(n)
			r.pending = buffer[:n]
			r.pendingPos = 0
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return grepLine{}, err
		}
		if n == 0 {
			return grepLine{}, &fsx.Error{Code: fsx.CodeSourceChanged}
		}
	}
}
