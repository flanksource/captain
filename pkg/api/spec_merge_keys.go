package api

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The struct tags a list uses to merge as a Kubernetes strategic merge patch —
// by element key rather than wholesale (see commons/merge).
const (
	patchStrategyTag = "patchStrategy"
	patchMergeKeyTag = "patchMergeKey"
)

var specType = reflect.TypeOf(Spec{})

// withCommitPhases returns s with every commit stanza's phase spelled out, on
// copies. `on` is the key commit stanzas merge by, and a stanza that omits it
// means run: resolving the default first lets a stanza that leaves its phase
// implicit meet the one that names it instead of failing for a missing key.
func (s Spec) withCommitPhases() Spec {
	if s.Workflow == nil || len(s.Workflow.Commits) == 0 {
		return s
	}
	workflow := *s.Workflow
	workflow.Commits = slices.Clone(workflow.Commits)
	for i := range workflow.Commits {
		workflow.Commits[i].On = workflow.Commits[i].Phase()
	}
	s.Workflow = &workflow
	return s
}

// splitPath splits a presence path into its escaped JSON-pointer tokens.
func splitPath(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

// keyedListDepth returns how many leading tokens of a presence path name the
// outermost list that merges by key, or 0 when no prefix of the path does.
func keyedListDepth(tokens []string) int {
	for depth := 1; depth <= len(tokens); depth++ {
		if _, keyed := keyedList(tokens[:depth]); keyed {
			return depth
		}
	}
	return 0
}

// keyedList reports whether tokens name a list that merges by key, and its key.
func keyedList(tokens []string) (string, bool) {
	field, ok := serializedStructField(specType, tokens)
	if !ok || field.Tag.Get(patchStrategyTag) == "" {
		return "", false
	}
	return field.Tag.Get(patchMergeKeyTag), true
}

// keyedListReplaces reports whether a path names a key-merged list, and if so
// whether an override's value there replaces the inherited list. Only an
// explicitly empty list does — it can only mean "clear". A list with elements is
// a patch the merge has already applied, and an empty one nobody wrote states
// nothing.
func keyedListReplaces(tokens []string, value reflect.Value, explicit bool) (keyed, replaces bool) {
	if _, keyed = keyedList(tokens); !keyed {
		return false, false
	}
	return true, explicit && value.IsValid() && value.Len() == 0
}

// inKeyedElement reports whether a presence path addresses something inside an
// element of a key-merged list, where positions differ from layer to merge.
func inKeyedElement(tokens []string) bool {
	depth := keyedListDepth(tokens)
	return depth > 0 && depth < len(tokens)
}

// keyedPresence rebuilds merged's explicit markers inside key-merged list
// elements from the operands', each re-addressed to its element's merged
// position. The structural merge unions the markers as recorded, at each
// operand's own positions, where they would name whichever element the merge
// happened to put there.
func (merged Spec) keyedPresence(operands ...Spec) FieldPresence {
	out := merged.Explicit.Clone()
	for path := range out {
		if inKeyedElement(splitPath(path)) {
			delete(out, path)
		}
	}
	for _, operand := range operands {
		for path, present := range operand.Explicit {
			if !inKeyedElement(splitPath(path)) {
				continue
			}
			if out == nil {
				out = FieldPresence{}
			}
			out[keyedPath(path, operand, merged)] = present
		}
	}
	return out
}

// serializedStructField is serializedField over types: it returns the struct
// field a presence path's last token names, so the field's tags — not its value
// — can decide how that path merges. ok is false when the path ends on a map
// entry, a list element, or nothing at all.
func serializedStructField(typ reflect.Type, tokens []string) (reflect.StructField, bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if len(tokens) == 0 {
		return reflect.StructField{}, false
	}
	switch typ.Kind() {
	case reflect.Struct:
		token := unescapeField(tokens[0])
		for i := range typ.NumField() {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" || !field.IsExported() {
				continue
			}
			if field.Anonymous && name == "" {
				if found, ok := serializedStructField(field.Type, tokens); ok {
					return found, true
				}
			} else if name == token {
				if len(tokens) == 1 {
					return field, true
				}
				return serializedStructField(field.Type, tokens[1:])
			}
		}
	case reflect.Map:
		return serializedStructField(typ.Elem(), tokens[1:])
	case reflect.Slice, reflect.Array:
		if _, err := strconv.Atoi(tokens[0]); err == nil {
			return serializedStructField(typ.Elem(), tokens[1:])
		}
	}
	return reflect.StructField{}, false
}

// keyedPath re-addresses a presence path from positions in `from` to positions
// in `to`. A key-merged list places each element by its key, so an element's
// index in one layer says nothing about its index in the merge: every index
// under such a list becomes the position of the element in `to` that shares the
// key of the element the path named in `from`. Other paths are returned as is.
//
// Both specs must have their merge keys resolved (see withCommitPhases). An
// element with no counterpart in `to` is a broken merge invariant, not a path to
// drop quietly, and panics naming it.
func keyedPath(path string, from, to Spec) string {
	tokens := splitPath(path)
	out := slices.Clone(tokens)
	for depth := 1; depth < len(tokens); depth++ {
		key, keyed := keyedList(tokens[:depth])
		if !keyed {
			continue
		}
		want := serializedField(reflect.ValueOf(from), append(slices.Clone(tokens[:depth+1]), key))
		if !want.IsValid() {
			panic(fmt.Sprintf("api: presence path %s names no element with a %q key", path, key))
		}
		list := serializedField(reflect.ValueOf(to), out[:depth])
		index := -1
		for i := 0; list.IsValid() && i < list.Len(); i++ {
			if got := serializedField(list.Index(i), []string{key}); got.IsValid() && reflect.DeepEqual(got.Interface(), want.Interface()) {
				index = i
				break
			}
		}
		if index < 0 {
			panic(fmt.Sprintf("api: presence path %s: no element of /%s has %s %v after the merge",
				path, strings.Join(out[:depth], "/"), key, want.Interface()))
		}
		out[depth] = strconv.Itoa(index)
	}
	return "/" + strings.Join(out, "/")
}

// rekeyed returns entries with every path inside a key-merged list element
// re-addressed from positions in `from` to positions in `to` (see keyedPath).
func rekeyed[V any](entries map[string]V, from, to Spec) map[string]V {
	if entries == nil {
		return nil
	}
	out := make(map[string]V, len(entries))
	for path, value := range entries {
		if inKeyedElement(splitPath(path)) {
			path = keyedPath(path, from, to)
		}
		out[path] = value
	}
	return out
}
