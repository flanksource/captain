package cli

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	clickyapi "github.com/flanksource/clicky/api"
	"github.com/sergi/go-diff/diffmatchpatch"
	"gopkg.in/yaml.v3"

	"github.com/flanksource/captain/pkg/api"
)

// `captain permissions convert` rewrites a prompt's or preset's permissions
// block into the portable access form, only when the conversion is exact or
// tighter. It prints the diff and writes nothing unless --write is passed; a
// conversion that would loosen anything is refused with the reason.

type PermissionsConvertOptions struct {
	File  string `args:"true" help:"A .prompt file or runtime preset YAML file"`
	Write bool   `flag:"write" help:"Rewrite the file in place instead of only printing the diff"`
}

type PermissionsConvertResult struct {
	File       string               `json:"file"`
	Conversion api.ConversionResult `json:"conversion"`
	Diff       string               `json:"diff"`
	Written    bool                 `json:"written"`
}

func RunPermissionsConvert(opts PermissionsConvertOptions) (any, error) {
	if strings.TrimSpace(opts.File) == "" {
		return nil, fmt.Errorf("a .prompt or preset file is required")
	}
	before, err := os.ReadFile(opts.File)
	if err != nil {
		return nil, err
	}
	conversion, after, err := convertPermissionsDocument(string(before))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", opts.File, err)
	}
	result := PermissionsConvertResult{File: opts.File, Conversion: conversion, Diff: lineDiff(string(before), after)}
	if opts.Write {
		info, err := os.Stat(opts.File)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(opts.File, []byte(after), info.Mode().Perm()); err != nil {
			return nil, err
		}
		result.Written = true
	}
	return result, nil
}

// yamlSegment is the YAML part of a document: a .prompt file's frontmatter or
// a whole preset file, with the line it starts on.
type yamlSegment struct {
	lines  []string
	start  int
	end    int
	mapped *yaml.Node
}

// convertPermissionsDocument converts the document's permissions (and, for the
// sandbox dialect, its sandbox) and splices the rewritten blocks into the
// original text, so every other line keeps its formatting and comments.
func convertPermissionsDocument(document string) (api.ConversionResult, string, error) {
	segment, err := parseYAMLSegment(document)
	if err != nil {
		return api.ConversionResult{}, "", err
	}
	spec, err := segment.permissionsSpec()
	if err != nil {
		return api.ConversionResult{}, "", err
	}
	conversion := api.ConvertToAccess(spec)
	if !conversion.Convertible {
		return conversion, "", fmt.Errorf("not converted: %s", conversion.Reason)
	}
	replacements := map[string]any{"permissions": conversion.Permissions}
	if conversion.Dialect == api.AccessDialectSandbox {
		replacements["sandbox"] = conversion.Sandbox
	}
	lines, err := segment.replaceKeys(replacements)
	if err != nil {
		return api.ConversionResult{}, "", err
	}
	return conversion, strings.Join(lines, "\n"), nil
}

func parseYAMLSegment(document string) (yamlSegment, error) {
	lines := strings.Split(document, "\n")
	segment := yamlSegment{lines: lines, start: 0, end: len(lines)}
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		closing := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				closing = i
				break
			}
		}
		if closing < 0 {
			return yamlSegment{}, fmt.Errorf("frontmatter opened with --- is never closed")
		}
		segment.start, segment.end = 1, closing
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[segment.start:segment.end], "\n")), &root); err != nil {
		return yamlSegment{}, fmt.Errorf("decode YAML: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return yamlSegment{}, fmt.Errorf("expected a YAML mapping")
	}
	segment.mapped = root.Content[0]
	// A runtime preset nests its spec under `spec:`; a .prompt keeps it at the top.
	if spec := mappingChild(segment.mapped, "spec"); spec != nil && spec.Kind == yaml.MappingNode {
		segment.mapped = spec
	}
	return segment, nil
}

func (s yamlSegment) permissionsSpec() (api.Spec, error) {
	var spec api.Spec
	node := mappingChild(s.mapped, "permissions")
	if node == nil {
		return spec, fmt.Errorf("no permissions block to convert")
	}
	if err := node.Decode(&spec.Permissions); err != nil {
		return spec, fmt.Errorf("decode permissions: %w", err)
	}
	if sandbox := mappingChild(s.mapped, "sandbox"); sandbox != nil {
		spec.Sandbox = &api.SandboxRef{}
		if err := sandbox.Decode(spec.Sandbox); err != nil {
			return spec, fmt.Errorf("decode sandbox: %w", err)
		}
	}
	return spec, nil
}

// replaceKeys rewrites each named key's block in place, working from the
// bottom of the document up so earlier line numbers stay valid.
func (s yamlSegment) replaceKeys(values map[string]any) ([]string, error) {
	lines := append([]string(nil), s.lines...)
	content := s.mapped.Content
	for i := len(content) - 2; i >= 0; i -= 2 {
		value, ok := values[content[i].Value]
		if !ok {
			continue
		}
		first := s.start + content[i].Line - 1
		last := s.end
		if i+2 < len(content) {
			last = s.start + content[i+2].Line - 1
		}
		for last > first+1 && strings.TrimSpace(lines[last-1]) == "" {
			last--
		}
		block, err := yamlBlock(content[i].Value, value, content[i].Column-1)
		if err != nil {
			return nil, err
		}
		lines = append(lines[:first], append(block, lines[last:]...)...)
	}
	return lines, nil
}

func yamlBlock(key string, value any, indent int) ([]string, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(map[string]any{key: value}); err != nil {
		return nil, fmt.Errorf("encode %s: %w", key, err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	block := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i := range block {
		block[i] = strings.Repeat(" ", indent) + block[i]
	}
	return block, nil
}

func mappingChild(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// lineDiff renders the changed lines only, prefixed - and +.
func lineDiff(before, after string) string {
	dmp := diffmatchpatch.New()
	left, right, lines := dmp.DiffLinesToChars(before, after)
	var out strings.Builder
	for _, diff := range dmp.DiffCharsToLines(dmp.DiffMain(left, right, false), lines) {
		prefix := map[diffmatchpatch.Operation]string{diffmatchpatch.DiffDelete: "-", diffmatchpatch.DiffInsert: "+"}[diff.Type]
		if prefix == "" {
			continue
		}
		for _, line := range strings.SplitAfter(diff.Text, "\n") {
			if line != "" {
				out.WriteString(prefix + strings.TrimSuffix(line, "\n") + "\n")
			}
		}
	}
	return out.String()
}

func (r PermissionsConvertResult) Pretty() clickyapi.Text {
	text := clickyapi.Text{}.Append(r.File, "font-bold").NewLine()
	for _, line := range strings.Split(strings.TrimRight(r.Diff, "\n"), "\n") {
		style := "text-green-500"
		if strings.HasPrefix(line, "-") {
			style = "text-red-500"
		}
		text = text.Append(line, style).NewLine()
	}
	for _, dropped := range r.Conversion.Dropped {
		text = text.Append("implied by read-only: "+dropped, "text-gray-500").NewLine()
	}
	for _, residual := range r.Conversion.Residual {
		text = text.Append("kept beside access: "+residual, "text-gray-500").NewLine()
	}
	if !r.Written {
		text = text.Append("not written; pass --write to rewrite the file", "text-gray-500")
	}
	return text
}
