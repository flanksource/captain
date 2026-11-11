package prompt

import (
	"slices"
	"strings"

	"github.com/mbleigh/raymond/parser"
)

func (t *Template) InputVariables() ([]string, error) {
	_, body, _ := splitFrontmatter(t.source)
	program, err := parser.Parse(body)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, path := range missingFrontmatterVariables(program, map[string]any{}) {
		name := strings.Split(path, ".")[0]
		if name == "" || name == "this" || strings.HasPrefix(name, "@") || strings.HasPrefix(name, "/") {
			continue
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}
