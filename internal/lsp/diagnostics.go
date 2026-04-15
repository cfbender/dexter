package lsp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.lsp.dev/protocol"

	"github.com/remoteoss/dexter/internal/parser"
	"github.com/remoteoss/dexter/internal/store"
	"github.com/remoteoss/dexter/internal/treesitter"
)

const (
	defaultDiagnosticsDebounceDelay = 150 * time.Millisecond
	minDiagnosticsDebounceDelay     = 25 * time.Millisecond
	maxDiagnosticsDebounceDelay     = 2 * time.Second
)

func (s *Server) scheduleDiagnostics(uri protocol.DocumentURI) {
	if s.client == nil {
		return
	}

	path := uriToPath(uri)
	if path == "" || !parser.IsElixirFile(path) || !s.isProjectFile(path) || s.isDepsFile(path) {
		return
	}

	text, ok := s.docs.Get(string(uri))
	if !ok {
		return
	}

	delay := s.getDiagnosticsDebounceDelay()

	s.diagMu.Lock()
	s.diagPending[uri] = text
	if timer, exists := s.diagTimers[uri]; exists {
		timer.Stop()
		timer.Reset(delay)
		s.diagMu.Unlock()
		return
	}

	s.diagTimers[uri] = time.AfterFunc(delay, func() {
		s.runQueuedDiagnostics(uri)
	})
	s.diagMu.Unlock()
}

func (s *Server) scheduleDiagnosticsForOpenDocs() {
	for _, docURI := range s.docs.URIs() {
		s.scheduleDiagnostics(protocol.DocumentURI(docURI))
	}
}

func (s *Server) getDiagnosticsDebounceDelay() time.Duration {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.diagDelay <= 0 {
		return defaultDiagnosticsDebounceDelay
	}
	return s.diagDelay
}

func (s *Server) setDiagnosticsDebounceDelay(delay time.Duration) {
	normalized := normalizeDiagnosticsDebounceDelay(delay)
	s.diagMu.Lock()
	s.diagDelay = normalized
	s.diagMu.Unlock()
}

func normalizeDiagnosticsDebounceDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return defaultDiagnosticsDebounceDelay
	}
	if delay < minDiagnosticsDebounceDelay {
		return minDiagnosticsDebounceDelay
	}
	if delay > maxDiagnosticsDebounceDelay {
		return maxDiagnosticsDebounceDelay
	}
	return delay
}

func parseDiagnosticsDebounceMS(raw interface{}) (time.Duration, bool) {
	if raw == nil {
		return 0, false
	}

	var ms int
	switch v := raw.(type) {
	case int:
		ms = v
	case int32:
		ms = int(v)
	case int64:
		ms = int(v)
	case float32:
		ms = int(v)
	case float64:
		ms = int(v)
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		ms = parsed
	default:
		return 0, false
	}

	return normalizeDiagnosticsDebounceDelay(time.Duration(ms) * time.Millisecond), true
}

func (s *Server) runQueuedDiagnostics(uri protocol.DocumentURI) {
	s.diagMu.Lock()
	snapshot, ok := s.diagPending[uri]
	delete(s.diagPending, uri)
	delete(s.diagTimers, uri)
	s.diagMu.Unlock()
	if !ok || s.client == nil {
		return
	}

	s.backgroundWork.Add(1)
	go func() {
		defer s.backgroundWork.Done()

		path := uriToPath(uri)
		diagnostics := s.buildDiagnosticsForDocument(uri, path, snapshot)
		if diagnostics == nil {
			diagnostics = []protocol.Diagnostic{}
		}

		current, ok := s.docs.Get(string(uri))
		if !ok || current != snapshot {
			return
		}

		_ = s.client.PublishDiagnostics(context.Background(), &protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: diagnostics,
		})
	}()
}

func (s *Server) buildDiagnosticsForDocument(uri protocol.DocumentURI, path, text string) []protocol.Diagnostic {
	var diagnostics []protocol.Diagnostic
	diagnostics = append(diagnostics, buildVariableDiagnostics([]byte(text))...)
	diagnostics = append(diagnostics, s.buildFunctionDiagnostics(uri, path, text)...)
	if diagnostics == nil {
		return []protocol.Diagnostic{}
	}
	return diagnostics
}

func (s *Server) buildFunctionDiagnostics(uri protocol.DocumentURI, path, text string) []protocol.Diagnostic {
	src := []byte(text)

	if path == "" {
		path = uriToPath(uri)
	}
	if path == "" {
		return nil
	}

	tf := s.docs.GetTokenizedFile(string(uri))
	if tf == nil {
		tf = NewTokenizedFile(text)
	}

	result := parser.TokenizeFull(src)
	tokens := result.Tokens
	lineStarts := result.LineStarts
	n := len(tokens)
	lines := strings.Split(text, "\n")
	fileAliases := ExtractAliases(text)
	hasUnknownProviders := s.hasUnknownImportOrUseProviders(tf, text, fileAliases)
	ignoredLines := make(map[int]bool)
	for i := 0; i < n; i++ {
		switch tokens[i].Kind {
		case parser.TokAttrSpec, parser.TokAttrType, parser.TokAttrCallback:
			ignoredLines[tokens[i].Line] = true
		}
	}

	diagnostics := make([]protocol.Diagnostic, 0)

	for i := 0; i < n; i++ {
		tok := tokens[i]
		if tok.Kind != parser.TokIdent {
			continue
		}

		name := parser.TokenText([]byte(text), tok)
		if name == "" {
			continue
		}

		callKind := bareCallKind(tokens, src, i)
		if callKind == 0 {
			continue
		}

		lineNum := tok.Line - 1 // 0-based
		if lineNum < 0 || lineNum >= len(lines) || lineNum >= len(lineStarts) {
			continue
		}
		if ignoredLines[tok.Line] {
			continue
		}

		startCol := tok.Start - lineStarts[lineNum]
		if startCol < 0 {
			continue
		}

		if callKind == 2 {
			visibleVars := treesitter.FindVariablesInScope(src, uint(lineNum), uint(startCol))
			if stringInSlice(visibleVars, name) {
				continue
			}
		}

		callArity, hasArity := inferBareCallArity(tokens, src, i, callKind)
		// Prefer open-buffer definitions first to avoid stale-index false positives.
		if _, found := tf.FindFunctionDefinition(name); found {
			continue
		}

		aliases := tf.ExtractAliasesInScope(lineNum)
		resolvedModule := s.resolveBareFunctionModule(path, text, lines, lineNum, name, aliases)
		if resolvedModule != "" {
			if !hasArity {
				continue
			}
			if defs, err := s.store.LookupFunctionInModules([]string{resolvedModule}, name, callArity); err == nil && len(defs) > 0 {
				continue
			}
			if defs := s.lookupThroughUseOf(resolvedModule, name); len(defs) > 0 {
				if hasArityInResults(defs, callArity) {
					continue
				}
				// Function exists via use-chain/provider module, but arity on
				// provider side may differ from direct module defs. Be conservative
				// and avoid false positives for external/imported APIs.
				continue
			}
			if defs := s.lookupThroughImportsOf(resolvedModule, name, map[string]bool{}); len(defs) > 0 {
				if hasArityInResults(defs, callArity) {
					continue
				}
				// Same conservative behavior for import-chain providers.
				continue
			}

			endCol := tok.End - lineStarts[lineNum]
			if endCol < startCol {
				continue
			}
			diagnostics = append(diagnostics, protocol.Diagnostic{
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(lineNum), Character: uint32(startCol)},
					End:   protocol.Position{Line: uint32(lineNum), Character: uint32(endCol)},
				},
				Severity: protocol.DiagnosticSeverityError,
				Source:   "dexter",
				Message:  fmt.Sprintf("undefined function: %s/%d", name, callArity),
			})
			continue
		}

		if callKind == 1 && hasUnknownProviders {
			continue
		}

		endCol := tok.End - lineStarts[lineNum]
		if startCol < 0 || endCol < startCol {
			continue
		}

		diagnostics = append(diagnostics, protocol.Diagnostic{
			Range: protocol.Range{
				Start: protocol.Position{Line: uint32(lineNum), Character: uint32(startCol)},
				End:   protocol.Position{Line: uint32(lineNum), Character: uint32(endCol)},
			},
			Severity: protocol.DiagnosticSeverityError,
			Source:   "dexter",
			Message:  "undefined function: " + name,
		})
	}

	return diagnostics
}

func (s *Server) hasUnknownImportOrUseProviders(tf *TokenizedFile, text string, aliases map[string]string) bool {
	for _, mod := range tf.ExtractImports() {
		resolved := resolveModule(mod, aliases)
		if results, err := s.store.LookupModule(resolved); err != nil || len(results) == 0 {
			return true
		}
	}

	for _, uc := range ExtractUsesWithOpts(text, aliases) {
		if results, err := s.store.LookupModule(uc.Module); err != nil || len(results) == 0 {
			return true
		}
	}

	return false
}

func bareCallKind(tokens []parser.Token, src []byte, i int) int {
	if i < 0 || i >= len(tokens) {
		return 0
	}
	tok := tokens[i]
	if tok.Kind != parser.TokIdent {
		return 0
	}

	prev := tokPrevSig(tokens, i-1)
	if prev >= 0 {
		switch tokens[prev].Kind {
		case parser.TokDot:
			return 0 // remote call: Module.fn(...)
		case parser.TokDef, parser.TokDefp, parser.TokDefmacro, parser.TokDefmacrop,
			parser.TokDefguard, parser.TokDefguardp, parser.TokDefdelegate,
			parser.TokAttrSpec, parser.TokAttrCallback:
			return 0 // definition heads/specs/callbacks
		}
	}

	next := tokNextSig(tokens, len(tokens), i+1)
	if next < len(tokens) && tokens[next].Kind == parser.TokOpenParen {
		return 1
	}

	// Pipe call: ... |> fn_name
	if prev >= 0 && tokens[prev].Kind == parser.TokPipe {
		if isPipeControlKeyword(parser.TokenText(src, tok)) {
			return 0
		}
		return 1
	}

	// Zero-arity bare call candidate: `value = missing`
	if isInFunctionHead(tokens, i) {
		return 0
	}
	if prev < 0 || !isZeroArityCallPrefix(tokens[prev], src) {
		return 0
	}

	immediate := i + 1
	if immediate >= len(tokens) {
		return 2
	}
	if tokens[immediate].Kind == parser.TokEOL || tokens[immediate].Kind == parser.TokEOF {
		return 2
	}
	if next >= len(tokens) {
		return 2
	}
	if isZeroArityCallBoundary(tokens[next], src) {
		return 2
	}

	return 0
}

func isInFunctionHead(tokens []parser.Token, i int) bool {
	if i < 0 || i >= len(tokens) {
		return false
	}
	line := tokens[i].Line
	for j := i - 1; j >= 0 && tokens[j].Line == line; j-- {
		switch tokens[j].Kind {
		case parser.TokDef, parser.TokDefp, parser.TokDefmacro, parser.TokDefmacrop,
			parser.TokDefguard, parser.TokDefguardp, parser.TokDefdelegate:
			return true
		case parser.TokDo:
			return false
		}
	}
	return false
}

func isZeroArityCallPrefix(tok parser.Token, src []byte) bool {
	if tok.Kind == parser.TokLeftArrow {
		return true
	}
	if tok.Kind != parser.TokOther {
		return false
	}
	return parser.TokenText(src, tok) == "="
}

func isZeroArityCallBoundary(tok parser.Token, src []byte) bool {
	switch tok.Kind {
	case parser.TokComma,
		parser.TokCloseParen,
		parser.TokCloseBracket,
		parser.TokCloseBrace,
		parser.TokRightArrow,
		parser.TokEnd,
		parser.TokEOF:
		return true
	case parser.TokOther:
		text := parser.TokenText(src, tok)
		return text == ";"
	default:
		return false
	}
}

func isPipeControlKeyword(name string) bool {
	switch name {
	case "case", "if", "unless", "cond", "with", "for", "try", "receive":
		return true
	default:
		return false
	}
}

func stringInSlice(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func hasArityInResults(results []store.LookupResult, arity int) bool {
	for _, r := range results {
		if r.Arity == arity {
			return true
		}
	}
	return false
}

func inferBareCallArity(tokens []parser.Token, src []byte, i int, callKind int) (int, bool) {
	if i < 0 || i >= len(tokens) {
		return 0, false
	}
	if callKind == 2 {
		return 0, true
	}
	next := tokNextSig(tokens, len(tokens), i+1)
	if next >= len(tokens) || tokens[next].Kind != parser.TokOpenParen {
		return 0, false
	}
	return countCallParenArity(tokens, src, next)
}

func countCallParenArity(tokens []parser.Token, _ []byte, openIdx int) (int, bool) {
	if openIdx < 0 || openIdx >= len(tokens) || tokens[openIdx].Kind != parser.TokOpenParen {
		return 0, false
	}

	depth := 0
	commas := 0
	hasArgToken := false
	for j := openIdx; j < len(tokens); j++ {
		tok := tokens[j]
		switch tok.Kind {
		case parser.TokOpenParen:
			depth++
			if depth == 1 {
				continue
			}
		case parser.TokCloseParen:
			depth--
			if depth == 0 {
				if !hasArgToken {
					return 0, true
				}
				return commas + 1, true
			}
		case parser.TokComma:
			if depth == 1 {
				commas++
				continue
			}
		}

		if depth == 1 {
			switch tok.Kind {
			case parser.TokEOL, parser.TokComment:
				continue
			default:
				hasArgToken = true
			}
		}
	}

	return 0, false
}

func tokPrevSig(tokens []parser.Token, i int) int {
	for ; i >= 0; i-- {
		if tokens[i].Kind != parser.TokEOL && tokens[i].Kind != parser.TokComment {
			return i
		}
	}
	return -1
}

func (s *Server) clearDiagnostics(uri protocol.DocumentURI) {
	s.cancelQueuedDiagnostics(uri)

	if s.client == nil {
		return
	}
	_ = s.client.PublishDiagnostics(context.Background(), &protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: []protocol.Diagnostic{},
	})
}

func (s *Server) cancelQueuedDiagnostics(uri protocol.DocumentURI) {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if timer, ok := s.diagTimers[uri]; ok {
		timer.Stop()
		delete(s.diagTimers, uri)
	}
	delete(s.diagPending, uri)
}

func (s *Server) stopAllDiagnosticsTimers() {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	for uri, timer := range s.diagTimers {
		timer.Stop()
		delete(s.diagTimers, uri)
	}
	for uri := range s.diagPending {
		delete(s.diagPending, uri)
	}
}

func buildVariableDiagnostics(src []byte) []protocol.Diagnostic {
	undefinedVars := treesitter.FindUndefinedVariables(src)
	if len(undefinedVars) == 0 {
		return nil
	}

	diagnostics := make([]protocol.Diagnostic, 0, len(undefinedVars))
	for _, unresolved := range undefinedVars {
		diagnostics = append(diagnostics, protocol.Diagnostic{
			Range: protocol.Range{
				Start: protocol.Position{Line: uint32(unresolved.Line), Character: uint32(unresolved.StartCol)},
				End:   protocol.Position{Line: uint32(unresolved.Line), Character: uint32(unresolved.EndCol)},
			},
			Severity: protocol.DiagnosticSeverityError,
			Source:   "dexter",
			Message:  "undefined variable: " + unresolved.Name,
		})
	}

	return diagnostics
}
