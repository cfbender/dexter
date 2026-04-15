package treesitter

import "testing"

func TestFindUndefinedVariables_Basic(t *testing.T) {
	src := []byte(`defmodule MyApp.Accounts do
  def run(data) do
    result = transform(data)
    missing + result
  end
end`)

	vars := FindUndefinedVariables(src)
	if len(vars) != 1 {
		t.Fatalf("expected 1 undefined variable, got %d", len(vars))
	}

	if vars[0].Name != "missing" {
		t.Fatalf("expected undefined variable 'missing', got %q", vars[0].Name)
	}
	if vars[0].Line != 3 {
		t.Fatalf("expected line 3, got %d", vars[0].Line)
	}
}

func TestFindUndefinedVariables_DoesNotReportBindings(t *testing.T) {
	src := []byte(`defmodule SharedLib.Worker do
  def process(item) do
    value = item
    with {:ok, next_item} <- fetch(item) do
      value + next_item
    end
  end
end`)

	vars := FindUndefinedVariables(src)
	if len(vars) != 0 {
		t.Fatalf("expected no undefined variables, got %d", len(vars))
	}
}

func TestFindUndefinedVariables_KeywordKeysIgnored(t *testing.T) {
	src := []byte(`defmodule MyApp.Accounts do
  def run(data) do
    %{status: data}
  end
end`)

	vars := FindUndefinedVariables(src)
	if len(vars) != 0 {
		t.Fatalf("expected no undefined variables, got %d", len(vars))
	}
}

func TestFindUndefinedVariables_StabPinnedValueIsReference(t *testing.T) {
	src := []byte(`defmodule MyApp.Accounts do
  def run(data) do
    Enum.map([1], fn ^missing -> data end)
  end
end`)

	vars := FindUndefinedVariables(src)
	if len(vars) != 1 {
		t.Fatalf("expected 1 undefined variable, got %d", len(vars))
	}
	if vars[0].Name != "missing" {
		t.Fatalf("expected undefined variable 'missing', got %q", vars[0].Name)
	}
}

func TestFindUndefinedVariables_FunctionCaptureNotVariable(t *testing.T) {
	src := []byte(`defmodule MyApp.Accounts do
  def my_function(test) do
    Enum.map(test, &is_test?/1)
  end

  defp is_test?(test), do: true
end`)

	vars := FindUndefinedVariables(src)
	if len(vars) != 0 {
		t.Fatalf("expected no undefined variables, got %d", len(vars))
	}
}

func TestFindUndefinedVariables_CaseGuardPatternBindingVisible(t *testing.T) {
	src := []byte(`defmodule SharedLib.Worker do
  def run(value) do
    case value do
      item when is_binary(item) and item != "" -> item
      _ -> value
    end
  end
end`)

	vars := FindUndefinedVariables(src)
	if len(vars) != 0 {
		t.Fatalf("expected no undefined variables, got %d", len(vars))
	}
}
