package lsp

import (
	"context"
	"testing"
	"time"

	"go.lsp.dev/protocol"
)

func TestParseDiagnosticsDebounceMS(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want time.Duration
		ok   bool
	}{
		{name: "int", in: 250, want: 250 * time.Millisecond, ok: true},
		{name: "float64", in: 80.0, want: 80 * time.Millisecond, ok: true},
		{name: "string", in: "120", want: 120 * time.Millisecond, ok: true},
		{name: "clamp low", in: 1, want: minDiagnosticsDebounceDelay, ok: true},
		{name: "clamp high", in: 9000, want: maxDiagnosticsDebounceDelay, ok: true},
		{name: "invalid", in: "abc", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseDiagnosticsDebounceMS(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok: expected %v, got %v", tt.ok, ok)
			}
			if got != tt.want {
				t.Fatalf("want %s, got %s", tt.want, got)
			}
		})
	}
}

func TestDidChangeConfiguration_UpdatesDiagnosticsDebounce(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	err := server.DidChangeConfiguration(context.Background(), &protocol.DidChangeConfigurationParams{
		Settings: map[string]interface{}{
			"dexter": map[string]interface{}{
				"diagnosticsDebounceMs": 420.0,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := server.getDiagnosticsDebounceDelay(); got != 420*time.Millisecond {
		t.Fatalf("expected debounce 420ms, got %s", got)
	}
}

func TestBuildVariableDiagnostics_UndefinedVariable(t *testing.T) {
	src := []byte(`defmodule MyApp.Accounts do
  def run(data) do
    missing + data
  end
end`)

	diagnostics := buildVariableDiagnostics(src)
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}

	d := diagnostics[0]
	if d.Message != "undefined variable: missing" {
		t.Fatalf("unexpected diagnostic message: %q", d.Message)
	}
	if d.Range.Start.Line != 2 {
		t.Fatalf("expected line 2, got %d", d.Range.Start.Line)
	}
}

func TestBuildVariableDiagnostics_NoUndefinedVariable(t *testing.T) {
	src := []byte(`defmodule SharedLib.Worker do
  def run(data) do
    value = data
    value
  end
end`)

	diagnostics := buildVariableDiagnostics(src)
	if len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostics, got %d", len(diagnostics))
	}
}

func TestCancelQueuedDiagnostics_ClearsPendingAndTimer(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	uri := protocol.DocumentURI("file:///tmp/example.ex")
	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	server.diagMu.Lock()
	server.diagPending[uri] = "defmodule X do\nend"
	server.diagTimers[uri] = timer
	server.diagMu.Unlock()

	server.cancelQueuedDiagnostics(uri)

	server.diagMu.Lock()
	_, hasPending := server.diagPending[uri]
	_, hasTimer := server.diagTimers[uri]
	server.diagMu.Unlock()

	if hasPending || hasTimer {
		t.Fatal("expected queued diagnostics state to be cleared")
	}
}

func TestStopAllDiagnosticsTimers_ClearsAllState(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	uriA := protocol.DocumentURI("file:///tmp/a.ex")
	uriB := protocol.DocumentURI("file:///tmp/b.ex")
	timerA := time.NewTimer(time.Second)
	timerB := time.NewTimer(time.Second)
	defer timerA.Stop()
	defer timerB.Stop()

	server.diagMu.Lock()
	server.diagPending[uriA] = "a"
	server.diagPending[uriB] = "b"
	server.diagTimers[uriA] = timerA
	server.diagTimers[uriB] = timerB
	server.diagMu.Unlock()

	server.stopAllDiagnosticsTimers()

	server.diagMu.Lock()
	pendingLen := len(server.diagPending)
	timersLen := len(server.diagTimers)
	server.diagMu.Unlock()

	if pendingLen != 0 || timersLen != 0 {
		t.Fatalf("expected empty diagnostics state, got pending=%d timers=%d", pendingLen, timersLen)
	}
}
