package history

import (
	"strconv"
	"strings"

	"github.com/t14raptor/go-fast/ast"
)

// codexScriptScope is the const/let environment used to fold a freeform exec
// script's literals into real values. Scripts build their commands from tables
// (`const cmds = [["git status", 5000], …]`) and destructure them inside a
// callback, so nothing resolves without following the bindings.
type codexScriptScope struct {
	parent *codexScriptScope
	values map[string]any
}

func newCodexScriptScope(parent *codexScriptScope) *codexScriptScope {
	return &codexScriptScope{parent: parent, values: map[string]any{}}
}

func (s *codexScriptScope) lookup(name string) (any, bool) {
	for scope := s; scope != nil; scope = scope.parent {
		if value, ok := scope.values[name]; ok {
			return value, true
		}
	}
	return nil, false
}

// bind destructures a binding target against a value. A target that does not
// match the value's shape is left unbound rather than bound to a guess: the
// unresolved name then fails the invocation loudly instead of rendering wrong.
func (s *codexScriptScope) bind(target *ast.Pattern, value any) {
	if target == nil {
		return
	}
	if identifier, ok := target.Identifier(); ok {
		s.values[identifier.Name] = value
		return
	}
	if assign, ok := target.Assign(); ok {
		if value == nil {
			if fallback, ok := evalCodexExpression(assign.Right, s); ok {
				s.bind(assign.Left, fallback)
			}
			return
		}
		s.bind(assign.Left, value)
		return
	}
	if array, ok := target.ArrayPat(); ok {
		s.bindArray(array, value)
		return
	}
	if object, ok := target.ObjectPat(); ok {
		s.bindObject(object, value)
	}
}

func (s *codexScriptScope) bindArray(pattern *ast.ArrayPattern, value any) {
	elements, ok := value.([]any)
	if !ok {
		return
	}
	for index := range pattern.Elements {
		if index >= len(elements) {
			break
		}
		s.bind(&pattern.Elements[index], elements[index])
	}
	if pattern.Rest != nil && len(pattern.Elements) <= len(elements) {
		s.bind(pattern.Rest, elements[len(pattern.Elements):])
	}
}

func (s *codexScriptScope) bindObject(pattern *ast.ObjectPattern, value any) {
	fields, ok := value.(map[string]any)
	if !ok {
		return
	}
	for index := range pattern.Properties {
		property := &pattern.Properties[index]
		if keyValue, ok := property.KeyValue(); ok {
			if key, ok := codexPropertyName(keyValue.Key, s); ok {
				s.bind(keyValue.Value, fields[key])
			}
			continue
		}
		if shorthand, ok := property.Shorthand(); ok {
			s.values[shorthand.Name.Name] = fields[shorthand.Name.Name]
		}
	}
}

func (v *codexExecVisitor) eval(expression *ast.Expression) (any, bool) {
	return evalCodexExpression(expression, v.scope)
}

func (v *codexExecVisitor) evalArray(expression *ast.Expression) ([]any, bool) {
	value, ok := evalCodexExpression(expression, v.scope)
	if !ok {
		return nil, false
	}
	elements, ok := value.([]any)
	return elements, ok
}

// evalCodexExpression evaluates the literal subset of JavaScript that command
// tables are built from. Anything outside it -- a call, a regexp, arithmetic on
// unknowns -- reports false so the caller can fail rather than invent a value.
func evalCodexExpression(expression *ast.Expression, scope *codexScriptScope) (any, bool) {
	if expression == nil {
		return nil, false
	}
	if literal, ok := expression.StringLit(); ok {
		return literal.Value, true
	}
	if literal, ok := expression.NumberLit(); ok {
		return literal.Value, true
	}
	if literal, ok := expression.BoolLit(); ok {
		return literal.Value, true
	}
	if _, ok := expression.NullLit(); ok {
		return nil, true
	}
	if identifier, ok := expression.Identifier(); ok {
		if identifier.Name == "undefined" {
			return nil, true
		}
		return scope.lookup(identifier.Name)
	}
	if await, ok := expression.Await(); ok {
		return evalCodexExpression(await.Argument, scope)
	}
	if optional, ok := expression.Optional(); ok {
		return evalCodexExpression(optional.Expr, scope)
	}
	if chain, ok := expression.OptionalChain(); ok {
		return evalCodexExpression(chain.Base, scope)
	}
	if array, ok := expression.ArrayLit(); ok {
		return evalCodexArray(array, scope)
	}
	if object, ok := expression.ObjectLit(); ok {
		return evalCodexObject(object, scope)
	}
	if template, ok := expression.TmplLit(); ok {
		return evalCodexTemplate(template, scope)
	}
	if member, ok := expression.Member(); ok {
		return evalCodexMember(member, scope)
	}
	if binary, ok := expression.Binary(); ok && binary.Operator == ast.BinaryAddition {
		return evalCodexConcatenation(binary, scope)
	}
	return nil, false
}

func evalCodexArray(array *ast.ArrayLiteral, scope *codexScriptScope) (any, bool) {
	values := make([]any, 0, len(array.Value))
	for index := range array.Value {
		element := &array.Value[index]
		if spread, ok := element.Spread(); ok {
			nested, ok := evalCodexExpression(spread.Expression, scope)
			if !ok {
				return nil, false
			}
			items, ok := nested.([]any)
			if !ok {
				return nil, false
			}
			values = append(values, items...)
			continue
		}
		value, ok := evalCodexExpression(element, scope)
		if !ok {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func evalCodexObject(object *ast.ObjectLiteral, scope *codexScriptScope) (any, bool) {
	fields := make(map[string]any, len(object.Value))
	for index := range object.Value {
		property := &object.Value[index]
		if spread, ok := property.Spread(); ok {
			nested, ok := evalCodexExpression(spread.Expression, scope)
			if !ok {
				return nil, false
			}
			merged, ok := nested.(map[string]any)
			if !ok {
				return nil, false
			}
			for key, value := range merged {
				fields[key] = value
			}
			continue
		}
		if keyValue, ok := property.KeyValue(); ok {
			key, ok := codexPropertyName(keyValue.Key, scope)
			if !ok {
				return nil, false
			}
			value, ok := evalCodexExpression(keyValue.Value, scope)
			if !ok {
				return nil, false
			}
			fields[key] = value
			continue
		}
		if shorthand, ok := property.Short(); ok {
			value, ok := scope.lookup(shorthand.Name.Name)
			if !ok {
				return nil, false
			}
			fields[shorthand.Name.Name] = value
			continue
		}
		return nil, false
	}
	return fields, true
}

func evalCodexTemplate(template *ast.TemplateLiteral, scope *codexScriptScope) (any, bool) {
	if template.Tag != nil {
		return nil, false
	}
	var value strings.Builder
	for index := range template.Elements {
		value.WriteString(template.Elements[index].Parsed)
		if index >= len(template.Expressions) {
			continue
		}
		resolved, ok := evalCodexExpression(&template.Expressions[index], scope)
		if !ok {
			return nil, false
		}
		text, ok := codexScriptString(resolved)
		if !ok {
			return nil, false
		}
		value.WriteString(text)
	}
	return value.String(), true
}

func evalCodexConcatenation(binary *ast.BinaryExpression, scope *codexScriptScope) (any, bool) {
	left, ok := evalCodexExpression(binary.Left, scope)
	if !ok {
		return nil, false
	}
	right, ok := evalCodexExpression(binary.Right, scope)
	if !ok {
		return nil, false
	}
	if leftNumber, ok := left.(float64); ok {
		if rightNumber, ok := right.(float64); ok {
			return leftNumber + rightNumber, true
		}
	}
	leftText, ok := codexScriptString(left)
	if !ok {
		return nil, false
	}
	rightText, ok := codexScriptString(right)
	if !ok {
		return nil, false
	}
	return leftText + rightText, true
}

func evalCodexMember(member *ast.MemberExpression, scope *codexScriptScope) (any, bool) {
	object, ok := evalCodexExpression(member.Object, scope)
	if !ok {
		return nil, false
	}
	if identifier, ok := member.Property.Identifier(); ok {
		return codexScriptField(object, identifier.Name)
	}
	computed, ok := member.Property.Computed()
	if !ok {
		return nil, false
	}
	key, ok := evalCodexExpression(computed.Expr, scope)
	if !ok {
		return nil, false
	}
	if index, ok := key.(float64); ok {
		items, ok := object.([]any)
		if !ok || int(index) < 0 || int(index) >= len(items) {
			return nil, false
		}
		return items[int(index)], true
	}
	name, ok := key.(string)
	if !ok {
		return nil, false
	}
	return codexScriptField(object, name)
}

func codexScriptField(object any, name string) (any, bool) {
	switch typed := object.(type) {
	case map[string]any:
		value, ok := typed[name]
		return value, ok
	case []any:
		if name == "length" {
			return float64(len(typed)), true
		}
	case string:
		if name == "length" {
			return float64(len([]rune(typed))), true
		}
	}
	return nil, false
}

func codexPropertyName(name *ast.PropertyName, scope *codexScriptScope) (string, bool) {
	if literal, ok := name.StringLit(); ok {
		return literal.Value, true
	}
	if literal, ok := name.NumberLit(); ok {
		return codexScriptNumber(literal.Value), true
	}
	computed, ok := name.Computed()
	if !ok {
		return "", false
	}
	value, ok := evalCodexExpression(computed.Expr, scope)
	if !ok {
		return "", false
	}
	return codexScriptString(value)
}

// codexScriptString renders a resolved value the way JavaScript would when it
// is interpolated into a string.
func codexScriptString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case float64:
		return codexScriptNumber(typed), true
	case bool:
		return strconv.FormatBool(typed), true
	case nil:
		return "null", true
	}
	return "", false
}

func codexScriptNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
