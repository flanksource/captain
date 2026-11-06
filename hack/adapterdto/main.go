// adapterdto preserves encoding/json's methods on generated native union types.
package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
)

func main() {
	paths, err := filepath.Glob("zz_generated_*.go")
	check(err)
	for _, path := range paths {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, parser.ParseComments)
		check(err)
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			selector, ok := declaration.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if ok && qualifier.Name == "json" && selector.Sel.Name == "RawMessage" {
				declaration.Assign = declaration.Name.End()
			}
			return true
		})
		output, err := os.Create(path)
		check(err)
		check(format.Node(output, set, file))
		check(output.Close())
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
