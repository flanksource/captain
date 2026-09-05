package prompt

import "fmt"

// Document renders frontmatter and returns its authored metadata and raw body.
// Model selectors remain as declared; provider and mode defaults are not applied.
func (t *Template) Document(data map[string]any) (*Document, error) {
	source, err := renderFrontmatter(t.source, data)
	if err != nil {
		return nil, fmt.Errorf("render prompt %s frontmatter: %w", t.name, err)
	}
	document, err := Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse prompt %s: %w", t.name, err)
	}
	return document, nil
}
