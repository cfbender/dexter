package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
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

func TestBuildFunctionDiagnostics_UndefinedBareFunction(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "my_module.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp.MyModule do
  def run(data) do
    missing_fn(data)
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}
	if diagnostics[0].Message != "undefined function: missing_fn" {
		t.Fatalf("unexpected diagnostic message: %q", diagnostics[0].Message)
	}
}

func TestBuildFunctionDiagnostics_UndefinedBareZeroArityFunction(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def run() do
    value = 1234
    other = missing
    {value, other}
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}
	if diagnostics[0].Message != "undefined function: missing" {
		t.Fatalf("unexpected diagnostic message: %q", diagnostics[0].Message)
	}
}

func TestBuildFunctionDiagnostics_DefinedLocalFunction_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "my_module.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp.MyModule do
  def run(data) do
    helper(data)
  end

  defp helper(data), do: data
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_RemoteCall_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "my_module.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp.MyModule do
  def run(data) do
    Enum.map(data, fn x -> x end)
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_TypespecTypeCalls_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  @spec run(any(), binary(), keyword()) :: any()
  def run(item, label \\ "", opts \\ []) do
    {item, label, opts}
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostics, got %d", len(diagnostics))
	}
}

func TestBuildFunctionDiagnostics_DefHeadPatternBinding_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def test(%Schema{} = value) do
    value.type
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_ImportedIndexedFunction_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	indexFile(t, server.store, server.projectRoot, "lib/utils.ex", `defmodule Utils do
  def is_value?(value), do: value != nil
end`)

	path := filepath.Join(server.projectRoot, "lib", "my_app.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp do
  import Utils

  def test(value) do
    is_value?(value)
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_ImportedIndexedFunctionWrongArity_Diagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	indexFile(t, server.store, server.projectRoot, "lib/utils.ex", `defmodule Utils do
  def is_value?(value), do: value != nil
end`)

	path := filepath.Join(server.projectRoot, "lib", "my_app.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp do
  import Utils

  def test(value) do
    is_value?(value, :extra)
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}
	if diagnostics[0].Message != "undefined function: is_value?/2" {
		t.Fatalf("unexpected diagnostic message: %q", diagnostics[0].Message)
	}
	if diagnostics[0].Range.Start.Line != 4 {
		t.Fatalf("expected line 4, got %d", diagnostics[0].Range.Start.Line)
	}
	if gotChar := diagnostics[0].Range.Start.Character; gotChar == 0 {
		t.Fatalf("expected non-zero start column, got %d", gotChar)
	}
}

func TestBuildFunctionDiagnostics_UnknownImportedModule_SuppressesUndefined(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "my_app.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp do
  import External.Lib

  def run(value) do
    external_fn(value)
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_ImportedFunctionViaUseChain_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query/api.ex", `defmodule External.Query.API do
  defmacro __using__(_opts) do
    quote do
      import External.Query.Builder
    end
  end
end`)
	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query/builder.ex", `defmodule External.Query.Builder do
  def from(expr, opts), do: {expr, opts}
end`)
	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query.ex", `defmodule External.Query do
  use External.Query.API
end`)

	path := filepath.Join(server.projectRoot, "lib", "my_app.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp do
  import External.Query

  def run(value) do
    from(value, [])
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_ImportedFunctionViaImportedChain_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query/api.ex", `defmodule External.Query.API do
  def from(expr, opts), do: {expr, opts}
  def assoc(expr, field), do: {expr, field}
end`)
	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query.ex", `defmodule External.Query do
  import External.Query.API
end`)

	path := filepath.Join(server.projectRoot, "lib", "my_app.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp do
  import External.Query

  def list_all(cool_schema) do
    from(c in cool_schema, left_join: a in assoc(c, :assoc_schema))
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_QueryDSLAssocInsideFrom_ResolvedViaQueryAPIConvention(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query/api.ex", `defmodule External.Query.API do
  def assoc(expr, field), do: {expr, field}
end`)
	indexFile(t, server.store, server.projectRoot, "deps/external_query/lib/external_query.ex", `defmodule External.Query do
  def from(expr, opts), do: {expr, opts}
end`)

	path := filepath.Join(server.projectRoot, "lib", "my_app.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule MyApp do
  import External.Query

  def list_all(cool_schema) do
    from(c in cool_schema, left_join: a in assoc(c, :assoc_schema))
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}

	lines := strings.Split(text, "\n")
	resolved := server.resolveBareFunctionModule(path, text, lines, 4, "assoc", ExtractAliases(text))
	if resolved != "External.Query.API" {
		t.Fatalf("expected assoc to resolve to External.Query.API, got %q", resolved)
	}
}

func TestBuildFunctionDiagnostics_QueryDSLAssocInsideFrom_ResolvedViaProviderRootFallback(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	indexFile(t, server.store, server.projectRoot, "deps/external/lib/external.ex", `defmodule External do
  def assoc(expr, field), do: {expr, field}
end`)
	indexFile(t, server.store, server.projectRoot, "deps/external/lib/external/query.ex", `defmodule External.Query do
  def from(expr, opts), do: {expr, opts}
end`)

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  import External.Query

  def run(queryable) do
    from q in assoc(queryable, :items), select: q.id
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}

	lines := strings.Split(text, "\n")
	resolved := server.resolveBareFunctionModule(path, text, lines, 5, "assoc", ExtractAliases(text))
	if resolved != "External" {
		t.Fatalf("expected assoc to resolve to External, got %q", resolved)
	}
}

func TestBuildFunctionDiagnostics_PipeIntoCase_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def run(input) do
    input
    |> case do
      :ok -> 1
      _ -> 0
    end
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_PipeIntoIf_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def run(input) do
    input
    |> if do
      :ok
    else
      :error
    end
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_PipeIntoCond_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def run(input) do
    input
    |> cond do
      input == :ok -> 1
      true -> 0
    end
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_PipeIntoWith_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def run(input) do
    input
    |> with do
      :ok <- input
      result = :ok
      result
    else
      _ -> :error
    end
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
	}
}

func TestBuildFunctionDiagnostics_PipeIntoTry_NoDiagnostic(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	path := filepath.Join(server.projectRoot, "lib", "worker.ex")
	docURI := protocol.DocumentURI(uri.File(path))
	text := `defmodule SharedLib.Worker do
  def run(input) do
    input
    |> try do
      _ -> :ok
    rescue
      _ -> :error
    end
  end
end`

	diagnostics := server.buildFunctionDiagnostics(docURI, path, text)
	if len(diagnostics) != 0 {
		d := diagnostics[0]
		t.Fatalf("expected no diagnostics, got %d; first=%q at %d:%d", len(diagnostics), d.Message, d.Range.Start.Line, d.Range.Start.Character)
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
