package lsp

import (
	"context"

	"go.lsp.dev/protocol"

	"github.com/remoteoss/dexter/internal/parser"
	"github.com/remoteoss/dexter/internal/treesitter"
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

	s.backgroundWork.Add(1)
	go func(snapshot string) {
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
	}(text)
}

func (s *Server) clearDiagnostics(uri protocol.DocumentURI) {
	if s.client == nil {
		return
	}
	_ = s.client.PublishDiagnostics(context.Background(), &protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: []protocol.Diagnostic{},
	})
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
