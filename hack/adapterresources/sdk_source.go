package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type sdkSource struct {
	fields map[string]string
	types  map[string]string
	enums  map[string][]any
}

func (b *sdkSchemas) source(path string) sdkSource {
	if source, exists := b.sources[path]; exists {
		return source
	}
	source := sdkSource{fields: map[string]string{}, types: map[string]string{}, enums: map[string][]any{}}
	if path == "" {
		return source
	}
	dir, err := exec.Command("go", "list", "-f", "{{.Dir}}", path).Output()
	must(err)
	files, err := os.ReadDir(strings.TrimSpace(string(dir)))
	must(err)
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(strings.TrimSpace(string(dir)), file.Name()), nil, parser.ParseComments)
		must(err)
		collectSDKDeclarations(parsed, source)
	}
	b.sources[path] = source
	return source
}

func collectSDKDeclarations(file *ast.File, source sdkSource) {
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		currentType := ""
		for _, raw := range group.Specs {
			switch spec := raw.(type) {
			case *ast.TypeSpec:
				source.types[spec.Name.Name] = group.Doc.Text()
				if structure, ok := spec.Type.(*ast.StructType); ok {
					for _, field := range structure.Fields.List {
						for _, name := range field.Names {
							source.fields[spec.Name.Name+"."+name.Name] = field.Doc.Text()
						}
					}
				}
			case *ast.ValueSpec:
				if group.Tok != token.CONST {
					continue
				}
				if kind, ok := spec.Type.(*ast.Ident); ok {
					currentType = kind.Name
				}
				for _, rawValue := range spec.Values {
					literal, ok := rawValue.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING || currentType == "" {
						continue
					}
					value, err := strconv.Unquote(literal.Value)
					must(err)
					source.enums[currentType] = append(source.enums[currentType], value)
				}
			}
		}
	}
}
