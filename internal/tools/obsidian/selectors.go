package obsidian

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"golang.org/x/text/unicode/norm"
)

type SourceSelectorKind string

const (
	SourceSelectorContent     SourceSelectorKind = "content"
	SourceSelectorHeading     SourceSelectorKind = "heading"
	SourceSelectorBlock       SourceSelectorKind = "block"
	SourceSelectorFrontmatter SourceSelectorKind = "frontmatter"
	SourceSelectorOutline     SourceSelectorKind = "outline"
)

var (
	ErrSourceSelectorInvalid = errors.New("invalid source selector")
	ErrSourceUnitNotFound    = errors.New("source unit not found")
	ErrSourceUnitAmbiguous   = errors.New("source unit is ambiguous")
)

// SourceSelector is the parser-facing closed selector shape. An empty Kind is
// the default content selector. Normalize validates that fields from different
// selector variants are not mixed.
type SourceSelector struct {
	Kind       SourceSelectorKind
	StartLine  int
	Heading    string
	Occurrence int
	BlockID    string
}

// SourceSelection identifies one source-preserving unit. Byte offsets are
// zero-based and EndByte is exclusive. Exactly one of Content and Outline is
// meaningful according to Selector.Kind.
type SourceSelection struct {
	Selector  SourceSelector
	StartByte int
	EndByte   int
	StartLine int
	EndLine   int
	Content   []byte
	Outline   []OutlineEntry
}

func (s SourceSelector) Normalize() (SourceSelector, error) {
	if s.Kind == "" {
		s.Kind = SourceSelectorContent
	}

	switch s.Kind {
	case SourceSelectorContent:
		if s.Heading != "" || s.Occurrence != 0 || s.BlockID != "" || s.StartLine < 0 {
			return SourceSelector{}, ErrSourceSelectorInvalid
		}
		if s.StartLine == 0 {
			s.StartLine = 1
		}
	case SourceSelectorHeading:
		if s.StartLine != 0 || s.BlockID != "" || s.Occurrence < 0 {
			return SourceSelector{}, ErrSourceSelectorInvalid
		}
		s.Heading = norm.NFC.String(strings.TrimSpace(s.Heading))
		if s.Heading == "" {
			return SourceSelector{}, ErrSourceSelectorInvalid
		}
		if s.Occurrence == 0 {
			s.Occurrence = 1
		}
	case SourceSelectorBlock:
		if s.StartLine != 0 || s.Heading != "" || s.Occurrence != 0 || !validBlockID(s.BlockID) {
			return SourceSelector{}, ErrSourceSelectorInvalid
		}
	case SourceSelectorFrontmatter, SourceSelectorOutline:
		if s.StartLine != 0 || s.Heading != "" || s.Occurrence != 0 || s.BlockID != "" {
			return SourceSelector{}, ErrSourceSelectorInvalid
		}
	default:
		return SourceSelector{}, ErrSourceSelectorInvalid
	}

	return s, nil
}

func validBlockID(id string) bool {
	if id == "" || strings.HasPrefix(id, "^") || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// Select returns one selector-defined source unit without rendering or
// reconstructing Markdown. Structural parsing is only invoked for heading,
// block, and outline selectors.
func (s *MarkdownSource) Select(selector SourceSelector) (SourceSelection, error) {
	normalized, err := selector.Normalize()
	if err != nil {
		return SourceSelection{}, err
	}

	switch normalized.Kind {
	case SourceSelectorContent:
		return s.selectContent(normalized)
	case SourceSelectorFrontmatter:
		return s.selectFrontmatter(normalized)
	case SourceSelectorHeading:
		return s.selectHeading(normalized)
	case SourceSelectorBlock:
		return s.selectBlock(normalized)
	case SourceSelectorOutline:
		return s.selectOutline(normalized), nil
	default:
		return SourceSelection{}, ErrSourceSelectorInvalid
	}
}

func (s *MarkdownSource) selectContent(selector SourceSelector) (SourceSelection, error) {
	if len(s.lines) == 0 {
		if selector.StartLine != 1 {
			return SourceSelection{}, ErrSourceUnitNotFound
		}
		return SourceSelection{
			Selector: selector, StartByte: 0, EndByte: 0, StartLine: 1, EndLine: 0,
			Content: []byte{},
		}, nil
	}
	line, ok := s.Line(selector.StartLine)
	if !ok {
		return SourceSelection{}, ErrSourceUnitNotFound
	}
	return s.contentSelection(selector, line.Start, len(s.source)), nil
}

func (s *MarkdownSource) selectFrontmatter(selector SourceSelector) (SourceSelection, error) {
	end, ok := s.frontmatterEnd()
	if !ok {
		return SourceSelection{}, ErrSourceUnitNotFound
	}
	return s.contentSelection(selector, 0, end), nil
}

func isFrontmatterDelimiter(line []byte, closing bool) bool {
	line = bytes.Trim(line, " \t")
	if bytes.Equal(line, []byte("---")) {
		return true
	}
	return closing && bytes.Equal(line, []byte("..."))
}

func (s *MarkdownSource) frontmatterEnd() (int, bool) {
	if len(s.lines) < 2 || !isFrontmatterDelimiter(s.lineContent(0), false) {
		return 0, false
	}
	for i := 1; i < len(s.lines); i++ {
		if isFrontmatterDelimiter(s.lineContent(i), true) {
			return s.lines[i].End, true
		}
	}
	return 0, false
}

func (s *MarkdownSource) selectHeading(selector SourceSelector) (SourceSelection, error) {
	structure := s.markdownStructure()
	wanted := selector.Occurrence
	for i, heading := range structure.headings {
		if heading.text != selector.Heading {
			continue
		}
		wanted--
		if wanted != 0 {
			continue
		}

		end := len(s.source)
		for _, next := range structure.headings[i+1:] {
			if next.level <= heading.level {
				end = next.start
				break
			}
		}
		return s.contentSelection(selector, heading.start, end), nil
	}
	return SourceSelection{}, ErrSourceUnitNotFound
}

func (s *MarkdownSource) selectBlock(selector SourceSelector) (SourceSelection, error) {
	if selection, err, handled := s.selectLargeBlockWithGoldmark(selector); handled {
		return selection, err
	}
	return s.selectBlockFromStructure(selector, s.markdownStructure())
}

func (s *MarkdownSource) selectBlockFromStructure(selector SourceSelector, structure markdownStructure) (SourceSelection, error) {
	var found *blockLocator
	for i := range structure.blocks {
		block := &structure.blocks[i]
		if block.id != selector.BlockID {
			continue
		}
		if found != nil {
			return SourceSelection{}, ErrSourceUnitAmbiguous
		}
		found = block
	}
	if found == nil {
		return SourceSelection{}, ErrSourceUnitNotFound
	}
	return s.contentSelection(selector, found.start, found.end), nil
}

const structuralPrefixMinLines = 4096

type rawBlockCandidate struct {
	line   int
	offset int
}

// selectLargeBlockWithGoldmark avoids parsing an irrelevant high-density tail
// without making Markdown decisions itself. The raw scan only discovers lines
// that contain the requested token. Goldmark classifies the leaf block that
// owns each candidate and must observe that block ending before the prefix
// boundary. If the bounded parse cannot prove closure, the cached full parse is
// authoritative.
func (s *MarkdownSource) selectLargeBlockWithGoldmark(selector SourceSelector) (SourceSelection, error, bool) {
	if len(s.lines) < structuralPrefixMinLines {
		return SourceSelection{}, nil, false
	}

	frontmatterEnd, _ := s.frontmatterEnd()
	candidates := make([]rawBlockCandidate, 0, 1)
	for index, line := range s.lines {
		if line.Start < frontmatterEnd {
			continue
		}
		id, ok := trailingBlockID(s.source[line.Start:line.ContentEnd])
		if ok && id == selector.BlockID {
			candidates = append(candidates, rawBlockCandidate{line: index, offset: line.Start})
		}
	}
	if len(candidates) == 0 {
		return SourceSelection{}, ErrSourceUnitNotFound, true
	}

	lastCandidateLine := candidates[len(candidates)-1].line
	boundaryLine := lastCandidateLine + 2
	if boundaryLine >= len(s.lines) {
		return SourceSelection{}, nil, false
	}
	prefixEnd := s.lines[boundaryLine].End
	if prefixEnd >= len(s.source) {
		return SourceSelection{}, nil, false
	}

	structure, resolved := s.parseMarkdownStructureThrough(prefixEnd, candidates)
	if !resolved {
		return SourceSelection{}, nil, false
	}
	selection, err := s.selectBlockFromStructure(selector, structure)
	return selection, err, true
}

func (s *MarkdownSource) selectOutline(selector SourceSelector) SourceSelection {
	structure := s.markdownStructure()
	outline := make([]OutlineEntry, len(structure.headings))
	for i, heading := range structure.headings {
		outline[i] = OutlineEntry{Line: heading.line, Level: heading.level, Text: heading.text}
	}
	startLine, endLine := s.selectionLines(0, len(s.source))
	return SourceSelection{
		Selector: selector, StartByte: 0, EndByte: len(s.source),
		StartLine: startLine, EndLine: endLine, Outline: outline,
	}
}

func (s *MarkdownSource) contentSelection(selector SourceSelector, start, end int) SourceSelection {
	startLine, endLine := s.selectionLines(start, end)
	return SourceSelection{
		Selector: selector, StartByte: start, EndByte: end,
		StartLine: startLine, EndLine: endLine, Content: s.source[start:end],
	}
}

func (s *MarkdownSource) lineContent(index int) []byte {
	line := s.lines[index]
	return s.source[line.Start:line.ContentEnd]
}

type markdownStructure struct {
	headings []headingLocator
	blocks   []blockLocator
}

type headingLocator struct {
	start int
	line  int
	level int
	text  string
}

type blockLocator struct {
	start int
	end   int
	id    string
}

var blockOnlyMarkdownParser = parser.NewParser(
	parser.WithBlockParsers(parser.DefaultBlockParsers()...),
)

func (s *MarkdownSource) markdownStructure() markdownStructure {
	s.structureOnce.Do(func() {
		s.structure = s.parseMarkdownStructure()
	})
	return s.structure
}

func (s *MarkdownSource) parseMarkdownStructure() markdownStructure {
	structure, _ := s.parseMarkdownStructureThrough(len(s.source), nil)
	return structure
}

func (s *MarkdownSource) parseMarkdownStructureThrough(parseEnd int, candidates []rawBlockCandidate) (markdownStructure, bool) {
	parseSource := s.source[:parseEnd]
	base := 0
	if frontmatterEnd, ok := s.frontmatterEnd(); ok {
		base = frontmatterEnd
		parseSource = s.source[frontmatterEnd:parseEnd]
	}
	root := blockOnlyMarkdownParser.Parse(text.NewReader(parseSource))
	structure := markdownStructure{}
	resolved := make([]bool, len(candidates))
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node.Type() != ast.TypeBlock {
			return ast.WalkContinue, nil
		}

		if heading, ok := node.(*ast.Heading); ok {
			if start, _, ok := s.rawNodeRange(node, true, parseSource, base); ok {
				structure.headings = append(structure.headings, headingLocator{
					start: start,
					line:  s.lineIndexAt(start) + 1,
					level: heading.Level,
					text:  headingText(heading, parseSource),
				})
			}
		}

		if hasBlockChild(node) {
			return ast.WalkContinue, nil
		}
		_, isHeading := node.(*ast.Heading)
		start, end, ok := s.rawNodeRange(node, isHeading, parseSource, base)
		if !ok {
			return ast.WalkContinue, nil
		}
		firstCandidate := sort.Search(len(candidates), func(i int) bool {
			return candidates[i].offset >= start
		})
		for i := firstCandidate; i < len(candidates) && candidates[i].offset < end; i++ {
			if end < parseEnd {
				resolved[i] = true
			}
		}
		if node.IsRaw() {
			return ast.WalkContinue, nil
		}
		if id, ok := trailingBlockID(s.source[start:end]); ok {
			structure.blocks = append(structure.blocks, blockLocator{start: start, end: end, id: id})
		}
		return ast.WalkContinue, nil
	})

	sort.SliceStable(structure.headings, func(i, j int) bool {
		return structure.headings[i].start < structure.headings[j].start
	})
	sort.SliceStable(structure.blocks, func(i, j int) bool {
		return structure.blocks[i].start < structure.blocks[j].start
	})
	for _, ok := range resolved {
		if !ok {
			return markdownStructure{}, false
		}
	}
	return structure, true
}

func hasBlockChild(node ast.Node) bool {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Type() == ast.TypeBlock {
			return true
		}
	}
	return false
}

func headingText(heading *ast.Heading, source []byte) string {
	return norm.NFC.String(strings.TrimSpace(string(heading.Lines().Value(source))))
}

// rawNodeRange expands goldmark's content-only segments to complete original
// physical lines. For Setext headings it also includes the underline line.
func (s *MarkdownSource) rawNodeRange(node ast.Node, heading bool, parseSource []byte, base int) (int, int, bool) {
	segments := node.Lines()
	var first, last int
	if segments.Len() == 0 {
		if node.Pos() < 0 || node.Pos() >= len(parseSource) {
			return 0, 0, false
		}
		first = s.lineIndexAt(base + node.Pos())
		last = first
	} else {
		first = s.lineIndexAt(base + segments.At(0).Start)
		last = first
		for i := 0; i < segments.Len(); i++ {
			segment := segments.At(i)
			position := base + segment.Start
			if segment.Stop > segment.Start {
				position = base + segment.Stop - 1
			}
			line := s.lineIndexAt(position)
			if line < first {
				first = line
			}
			if line > last {
				last = line
			}
		}
	}

	if heading && isSetextHeading(node) && last+1 < len(s.lines) {
		last++
	}
	return s.lines[first].Start, s.lines[last].End, true
}

func isSetextHeading(node ast.Node) bool {
	heading, ok := node.(*ast.Heading)
	if !ok || heading.Lines().Len() == 0 {
		return false
	}
	return node.Pos() == heading.Lines().At(0).Start
}

func trailingBlockID(source []byte) (string, bool) {
	source = bytes.TrimRightFunc(source, unicode.IsSpace)
	if len(source) == 0 {
		return "", false
	}
	start := len(source)
	for start > 0 {
		r, size := utf8.DecodeLastRune(source[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	token := source[start:]
	if len(token) < 2 || token[0] != '^' {
		return "", false
	}
	id := string(token[1:])
	if !validBlockID(id) {
		return "", false
	}
	return id, true
}
