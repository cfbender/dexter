package lsp

import (
	"context"
	"strconv"
	"time"

	"go.lsp.dev/protocol"

	"github.com/remoteoss/dexter/internal/parser"
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

		diagnostics := buildVariableDiagnostics([]byte(snapshot))

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
