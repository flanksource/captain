package prompt

import (
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/mbleigh/raymond"
	"github.com/mbleigh/raymond/ast"
)

type variableScope struct {
	value   any
	aliases map[string]any
	data    map[string]any
}

type variableCheck struct {
	scopes  []variableScope
	missing map[string]bool
}

func missingFrontmatterVariables(program *ast.Program, data map[string]any) []string {
	check := variableCheck{scopes: []variableScope{{value: data}}, missing: map[string]bool{}}
	check.visit(program)
	missing := make([]string, 0, len(check.missing))
	for name := range check.missing {
		missing = append(missing, name)
	}
	slices.Sort(missing)
	return missing
}

func (c *variableCheck) visit(node ast.Node) {
	switch node := node.(type) {
	case *ast.Program:
		if node != nil {
			for _, child := range node.Body {
				c.visit(child)
			}
		}
	case *ast.MustacheStatement:
		c.visit(node.Expression)
	case *ast.Expression:
		if len(node.Params) == 0 && node.Hash == nil {
			c.visit(node.Path)
		}
		for _, param := range node.Params {
			c.visit(param)
		}
		if node.Hash != nil {
			for _, pair := range node.Hash.Pairs {
				c.visit(pair.Val)
			}
		}
	case *ast.SubExpression:
		c.visit(node.Expression)
	case *ast.PathExpression:
		if _, exists := c.value(node); !exists {
			c.missing[node.Original] = true
		}
	case *ast.BlockStatement:
		c.visitBlock(node)
	case *ast.PartialStatement:
		for _, param := range node.Params {
			c.visit(param)
		}
	}
}

func (c *variableCheck) visitBlock(block *ast.BlockStatement) {
	c.visit(block.Expression)
	if len(block.Expression.Params) == 0 {
		c.visit(block.Program)
		c.visit(block.Inverse)
		return
	}
	value, known := c.value(block.Expression.Params[0])
	if !known {
		c.visit(block.Program)
		c.visit(block.Inverse)
		return
	}
	switch block.Expression.HelperName() {
	case "if", "unless":
		truth := raymond.IsTrue(value)
		if block.Expression.HelperName() == "unless" {
			truth = !truth
		}
		if truth {
			c.visit(block.Program)
		} else {
			c.visit(block.Inverse)
		}
	case "with":
		if raymond.IsTrue(value) {
			aliases := map[string]any{}
			if len(block.Program.BlockParams) > 0 {
				aliases[block.Program.BlockParams[0]] = value
			}
			c.visitScope(block.Program, variableScope{value: value, aliases: aliases})
		} else {
			c.visit(block.Inverse)
		}
	case "each":
		c.visitEach(block, value)
	default:
		c.visit(block.Program)
		c.visit(block.Inverse)
	}
}

func (c *variableCheck) visitEach(block *ast.BlockStatement, value any) {
	items := reflect.ValueOf(value)
	if !items.IsValid() || (items.Kind() != reflect.Map && items.Kind() != reflect.Slice && items.Kind() != reflect.Array) || items.Len() == 0 {
		c.visit(block.Inverse)
		return
	}
	var keys []reflect.Value
	if items.Kind() == reflect.Map {
		keys = items.MapKeys()
	}
	for i := 0; i < items.Len(); i++ {
		var item, key any
		if items.Kind() == reflect.Map {
			mapKey := keys[i]
			key, item = mapKey.Interface(), items.MapIndex(mapKey).Interface()
		} else {
			key, item = i, items.Index(i).Interface()
		}
		aliases := map[string]any{}
		for j, name := range block.Program.BlockParams {
			if j == 0 {
				aliases[name] = item
			} else {
				aliases[name] = key
			}
		}
		data := map[string]any{"index": i, "key": key, "first": i == 0, "last": i == items.Len()-1}
		c.visitScope(block.Program, variableScope{value: item, aliases: aliases, data: data})
	}
}

func (c *variableCheck) visitScope(program *ast.Program, scope variableScope) {
	c.scopes = append(c.scopes, scope)
	c.visit(program)
	c.scopes = c.scopes[:len(c.scopes)-1]
}

func (c *variableCheck) value(node ast.Node) (any, bool) {
	switch node := node.(type) {
	case *ast.PathExpression:
		return c.pathValue(node)
	case *ast.StringLiteral:
		return node.Value, true
	case *ast.BooleanLiteral:
		return node.Value, true
	case *ast.NumberLiteral:
		return node.Number(), true
	case *ast.SubExpression:
		if node.Expression.HelperName() == "lookup" && len(node.Expression.Params) == 2 {
			value, valueKnown := c.value(node.Expression.Params[0])
			key, keyKnown := c.value(node.Expression.Params[1])
			if valueKnown && keyKnown {
				return variableProperty(value, raymond.Str(key))
			}
		}
		return nil, false
	default:
		return nil, false
	}
}

func (c *variableCheck) pathValue(path *ast.PathExpression) (any, bool) {
	index := len(c.scopes) - 1 - path.Depth
	parts := path.Parts
	if path.Data {
		if len(parts) > 0 && parts[0] == "root" {
			index, parts = 0, parts[1:]
		} else {
			for i := index; i >= 0; i-- {
				if value, exists := c.scopes[i].data[strings.Join(parts, ".")]; exists {
					return value, true
				}
			}
			return nil, false
		}
	}
	if index < 0 {
		return nil, false
	}
	value := c.scopes[index].value
	if len(parts) > 0 {
		for i := index; i >= 0; i-- {
			if alias, exists := c.scopes[i].aliases[parts[0]]; exists {
				value, parts = alias, parts[1:]
				break
			}
		}
	}
	for _, part := range parts {
		var exists bool
		value, exists = variableProperty(value, part)
		if !exists {
			return nil, false
		}
	}
	return value, true
}

func variableProperty(value any, name string) (any, bool) {
	v := reflect.ValueOf(value)
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Map:
		key := reflect.ValueOf(name)
		if key.Type().AssignableTo(v.Type().Key()) {
			v = v.MapIndex(key)
		} else {
			return nil, false
		}
	case reflect.Slice, reflect.Array:
		index, err := strconv.Atoi(name)
		if err != nil || index < 0 || index >= v.Len() {
			return nil, false
		}
		v = v.Index(index)
	case reflect.Struct:
		v = v.FieldByName(name)
	default:
		return nil, false
	}
	if !v.IsValid() || !v.CanInterface() {
		return nil, false
	}
	return v.Interface(), true
}
