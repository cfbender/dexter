package treesitter

import (
	"slices"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// UndefinedVariable is one unresolved variable usage in source.
type UndefinedVariable struct {
	Name     string
	Line     uint // 0-based
	StartCol uint // 0-based
	EndCol   uint // 0-based, exclusive
}

// FindUndefinedVariables returns unresolved variable usages in function scope.
//
// This is intentionally conservative for first-pass diagnostics:
//   - only identifier usages (not bindings) are checked
//   - variables visible at cursor position are resolved via FindVariablesInScopeWithTree
//   - only checks inside function scopes
func FindUndefinedVariables(src []byte) []UndefinedVariable {
	root, cleanup := parseElixir(src)
	if root == nil {
		return nil
	}
	defer cleanup()

	var out []UndefinedVariable
	collectUndefinedVariables(root, root, src, &out)
	return out
}

func collectUndefinedVariables(root, node *tree_sitter.Node, src []byte, out *[]UndefinedVariable) {
	if node == nil {
		return
	}

	if node.Kind() == "identifier" {
		name := node.Utf8Text(src)
		if shouldCheckIdentifierForUndefined(node, src, name) {
			line := uint(node.StartPosition().Row)
			col := uint(node.StartPosition().Column)
			visible := FindVariablesInScopeWithTree(root, src, line, col)
			if len(visible) > 0 && !containsString(visible, name) {
				*out = append(*out, UndefinedVariable{
					Name:     name,
					Line:     line,
					StartCol: col,
					EndCol:   uint(node.EndPosition().Column),
				})
			}
		}
	}

	for i := uint(0); i < uint(node.ChildCount()); i++ {
		collectUndefinedVariables(root, node.Child(i), src, out)
	}
}

func shouldCheckIdentifierForUndefined(node *tree_sitter.Node, src []byte, name string) bool {
	if name == "" || isDefinitionKeyword(name) {
		return false
	}
	if isFunctionNameInCall(node, src) {
		return false
	}
	if isRemoteFunctionNameInCall(node) {
		return false
	}
	if isModuleAttributeIdent(node, src) {
		return false
	}
	if isBindingIdentifier(node, src) {
		return false
	}
	if isKeywordKeyIdentifier(node, src) {
		return false
	}
	return true
}

func isRemoteFunctionNameInCall(node *tree_sitter.Node) bool {
	parent := node.Parent()
	if parent == nil || parent.Kind() != "dot" {
		return false
	}

	// In remote calls like `Enum.map(x)`, `map` is the last child of `dot`.
	if parent.ChildCount() == 0 {
		return false
	}
	last := parent.Child(parent.ChildCount() - 1)
	if last.StartByte() != node.StartByte() || last.EndByte() != node.EndByte() {
		return false
	}

	grandparent := parent.Parent()
	if grandparent == nil || grandparent.Kind() != "call" || grandparent.ChildCount() == 0 {
		return false
	}
	first := grandparent.Child(0)
	return first.StartByte() == parent.StartByte() && first.EndByte() == parent.EndByte()
}

func isBindingIdentifier(node *tree_sitter.Node, src []byte) bool {
	if isAssignmentTarget(node, src) {
		return true
	}
	if isInDefHead(node, src) {
		return true
	}
	if isInStabArgumentsUnpinned(node, src) {
		return true
	}
	if isInArrowLeftPatternUnpinned(node, src) {
		return true
	}
	return false
}

func isInDefHead(node *tree_sitter.Node, src []byte) bool {
	current := node.Parent()
	for current != nil {
		if current.Kind() == "call" && current.ChildCount() > 1 {
			first := current.Child(0)
			if first.Kind() == "identifier" && functionKeywords[first.Utf8Text(src)] {
				head := current.Child(1)
				return node.StartByte() >= head.StartByte() && node.EndByte() <= head.EndByte()
			}
		}
		current = current.Parent()
	}
	return false
}

func isInStabArgumentsUnpinned(node *tree_sitter.Node, src []byte) bool {
	current := node.Parent()
	for current != nil {
		if current.Kind() == "arguments" {
			parent := current.Parent()
			if parent != nil && parent.Kind() == "stab_clause" {
				if node.StartByte() >= current.StartByte() && node.EndByte() <= current.EndByte() {
					return !isPinnedIdentifier(node, src, current)
				}
			}
		}
		current = current.Parent()
	}
	return false
}

func isInArrowLeftPatternUnpinned(node *tree_sitter.Node, src []byte) bool {
	current := node.Parent()
	for current != nil {
		if current.Kind() == "binary_operator" && current.ChildCount() >= 3 {
			op := current.Child(1).Utf8Text(src)
			if op == "<-" {
				lhs := current.Child(0)
				if node.StartByte() >= lhs.StartByte() && node.EndByte() <= lhs.EndByte() {
					return !isPinnedIdentifier(node, src, lhs)
				}
			}
		}
		current = current.Parent()
	}
	return false
}

func isPinnedIdentifier(node *tree_sitter.Node, src []byte, boundary *tree_sitter.Node) bool {
	current := node.Parent()
	for current != nil {
		if isPinOperator(current, src) {
			return true
		}
		if boundary != nil && current.StartByte() == boundary.StartByte() && current.EndByte() == boundary.EndByte() {
			break
		}
		current = current.Parent()
	}
	return false
}

func isKeywordKeyIdentifier(node *tree_sitter.Node, src []byte) bool {
	end := int(node.EndByte())
	if end < 0 || end >= len(src) {
		return false
	}
	if src[end] != ':' {
		return false
	}
	if end+1 < len(src) && src[end+1] == ':' {
		return false
	}
	return true
}

func containsString(items []string, target string) bool {
	return slices.Contains(items, target)
}
