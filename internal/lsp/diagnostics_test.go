package lsp

import "testing"

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
