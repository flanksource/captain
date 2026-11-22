package session

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// InputMetadata is an initial prompt a host wrote as XML fields rather than
// prose — Claude's <task-notification> re-invocations and slash-command
// <command-name>/<command-args> blocks. Tag names the single root element and
// is empty when the prompt is a run of sibling elements; Fields holds every
// leaf element's text.
type InputMetadata struct {
	Tag    string            `json:"tag,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	title  string
}

// inputTitleFields are the leaf elements that describe the input, in the
// order a title prefers them.
var inputTitleFields = []string{"summary", "title", "subject", "description", "message", "command-message"}

type xmlElement struct {
	name     xml.Name
	text     strings.Builder
	hasChild bool
}

// ParseInputMetadata reads a prompt made only of XML elements. Prose that
// merely mentions a tag, mixed text-and-element content, and malformed markup
// are not inputs of this shape, so they return nil.
func ParseInputMetadata(prompt string) *InputMetadata {
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, "<") || !strings.HasSuffix(trimmed, ">") {
		return nil
	}
	decoder := xml.NewDecoder(strings.NewReader(trimmed))
	decoder.Strict = false
	var stack []*xmlElement
	var roots, order []string
	parents := 0
	fields := map[string]string{}
	for {
		// RawToken preserves mismatched tags while non-strict decoding allows bare &.
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil
		}
		switch t := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				roots = append(roots, t.Name.Local)
			} else {
				stack[len(stack)-1].hasChild = true
			}
			stack = append(stack, &xmlElement{name: t.Name})
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != t.Name {
				return nil
			}
			element := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			text := strings.TrimSpace(element.text.String())
			if element.hasChild && text != "" {
				return nil
			}
			if len(stack) == 0 && element.hasChild {
				parents++
			}
			name := element.name.Local
			if _, seen := fields[name]; !element.hasChild && text != "" && !seen {
				fields[name] = text
				order = append(order, name)
			}
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil
				}
				continue
			}
			stack[len(stack)-1].text.Write(t)
		}
	}
	if len(stack) > 0 || len(order) == 0 {
		return nil
	}
	metadata := &InputMetadata{Fields: fields}
	if len(roots) == 1 && parents == 1 {
		metadata.Tag = roots[0]
	}
	metadata.title = inputTitle(metadata, order)
	return metadata
}

func inputTitle(metadata *InputMetadata, order []string) string {
	for _, key := range inputTitleFields {
		if value := metadata.Fields[key]; value != "" {
			return value
		}
	}
	if metadata.Tag != "" {
		return strings.ReplaceAll(metadata.Tag, "-", " ")
	}
	return metadata.Fields[order[0]]
}
