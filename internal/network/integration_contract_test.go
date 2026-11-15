package network_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Keep new egress clients from bypassing the shared constructor. The two native
// MCP-host clients are private authenticated IPC, not business-network clients.
func TestApplicationClientsDeclareNetworkScope(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(source))
	positions := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Dir(path) == filepath.Dir(source) {
			return nil
		}
		file, err := parser.ParseFile(positions, path, nil, 0)
		if err != nil {
			return err
		}
		httpName, networkName := "", ""
		for _, imp := range file.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			alias := filepath.Base(name)
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			if name == "net/http" {
				httpName = alias
			}
			if name == "vista/internal/network" {
				networkName = alias
			}
		}
		if httpName == "" {
			return nil
		}
		var stack []ast.Node
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, node)
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			kind, ok := literal.Type.(*ast.SelectorExpr)
			if !ok || kind.Sel.Name != "Client" {
				return true
			}
			pkg, ok := kind.X.(*ast.Ident)
			if !ok || pkg.Name != httpName {
				return true
			}
			for _, parent := range stack {
				if fn, ok := parent.(*ast.FuncDecl); ok && filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "mcphost/client.go")) && (fn.Name.Name == "Acquire" || fn.Name.Name == "post") {
					return true
				}
				call, ok := parent.(*ast.CallExpr)
				if !ok {
					continue
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := method.X.(*ast.Ident)
				if ok && networkName != "" && pkg.Name == networkName && (method.Sel.Name == "NewClient" || method.Sel.Name == "ClientFor") {
					return true
				}
			}
			t.Errorf("%s: application HTTP client must use network.NewClient with an explicit scope", positions.Position(literal.Pos()))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
