package api

import (
	"fmt"
	"path/filepath"
)

// Validate reports a decision the request cannot take. It runs where a host
// resolves an approval, so a bad answer is refused to the host instead of
// becoming a terminal row the provider then fails to translate.
func (d ApprovalDecision) Validate(req ApprovalRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if d.Interrupt && (d.Allow || !req.Interruptible) {
		return fmt.Errorf("%s approval for %q cannot interrupt the turn: interrupt needs a deny on an interruptible request", req.Kind, req.Tool)
	}
	if d.Scope != "" && d.Scope != ApprovalScopeRequest && (!d.Allow || !req.supportsScope(d.Scope)) {
		return fmt.Errorf("%s approval for %q does not offer scope %q for this decision", req.Kind, req.Tool, d.Scope)
	}
	if d.Grants != nil && (req.Kind != ApprovalKindPermissions || !d.Allow) {
		return fmt.Errorf("grants only answer an approved permissions request, not a %s request for %q", req.Kind, req.Tool)
	}
	if !d.Allow {
		return nil
	}
	switch req.Kind {
	case ApprovalKindPermissions:
		if d.UpdatedInput != nil {
			return fmt.Errorf("a permissions approval takes grants, not updatedInput")
		}
		if d.Grants != nil {
			return grantWithin(*d.Grants, *req.Permissions)
		}
	case ApprovalKindQuestion:
		if answers, ok := d.UpdatedInput["answers"]; ok {
			if _, err := AnswersForQuestions(req.Questions, answers); err != nil {
				return fmt.Errorf("answers for %q: %w", req.Tool, err)
			}
		}
	case ApprovalKindElicitation:
		return validateElicitationContent(*req.Elicitation, d.UpdatedInput)
	case ApprovalKindPlan:
		if d.UpdatedInput != nil {
			return fmt.Errorf("a plan approval runs the plan as written and takes no updatedInput; deny with feedback to change it")
		}
	}
	return nil
}

// grantWithin rejects a grant that names anything the request did not ask for.
func grantWithin(grant, requested NativeSandboxPolicy) error {
	if grant.Required != nil || grant.Commands != nil || grant.Credentials != nil || grant.Platform != nil {
		return fmt.Errorf("a permissions grant may only carry filesystem and network, not commands, credentials, platform or required")
	}
	if fs := grant.Filesystem; fs != nil {
		if fs.Access != "" || len(fs.DeniedReadRoots) > 0 || len(fs.DeniedWriteRoots) > 0 || fs.IncludeSystemTemp != nil {
			return fmt.Errorf("a filesystem grant may only list writableRoots and readableRoots")
		}
		asked := requested.Filesystem
		if asked == nil {
			asked = &SandboxFilesystemPolicy{}
		}
		if err := pathsWithin("writable root", fs.WritableRoots, asked.WritableRoots); err != nil {
			return err
		}
		if err := pathsWithin("readable root", fs.ReadableRoots, append(append([]string{}, asked.ReadableRoots...), asked.WritableRoots...)); err != nil {
			return err
		}
	}
	if network := grant.Network; network != nil {
		asked := requested.Network
		if asked == nil {
			asked = &SandboxNetworkPolicy{}
		}
		if network.Access != "" && network.Access != asked.Access {
			return fmt.Errorf("network access %q was not requested (requested %q)", network.Access, asked.Access)
		}
		if err := valuesWithin("domain", network.AllowedDomains, asked.AllowedDomains); err != nil {
			return err
		}
	}
	return nil
}

func pathsWithin(label string, granted, requested []string) error {
	clean := make([]string, len(requested))
	for i, path := range requested {
		clean[i] = filepath.Clean(path)
	}
	cleanGranted := make([]string, len(granted))
	for i, path := range granted {
		cleanGranted[i] = filepath.Clean(path)
	}
	return valuesWithin(label, cleanGranted, clean)
}

func valuesWithin(label string, granted, requested []string) error {
	asked := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		asked[value] = struct{}{}
	}
	for _, value := range granted {
		if _, ok := asked[value]; !ok {
			return fmt.Errorf("%s %q was not requested", label, value)
		}
	}
	return nil
}

// validateElicitationContent checks accepted content against an MCP form
// schema, which MCP restricts to a flat object of primitive properties.
func validateElicitationContent(elicitation ElicitationApproval, content map[string]any) error {
	if elicitation.Mode == ElicitationModeURL {
		if content != nil {
			return fmt.Errorf("a url-mode elicitation from %q is completed at its url and takes no content", elicitation.Server)
		}
		return nil
	}
	if content == nil {
		return fmt.Errorf("accepting the %q form needs content", elicitation.Server)
	}
	properties, _ := elicitation.Schema["properties"].(map[string]any)
	for _, field := range sortedKeys(content) {
		schema, ok := properties[field].(map[string]any)
		if !ok {
			return fmt.Errorf("elicitation %q declares no field %q", elicitation.Server, field)
		}
		if err := primitiveMatches(field, schema, content[field]); err != nil {
			return fmt.Errorf("elicitation %q: %w", elicitation.Server, err)
		}
	}
	required, _ := elicitation.Schema["required"].([]any)
	for _, name := range required {
		field, _ := name.(string)
		if _, ok := content[field]; !ok {
			return fmt.Errorf("elicitation %q: required field %q is missing", elicitation.Server, field)
		}
	}
	return nil
}

func primitiveMatches(field string, schema map[string]any, value any) error {
	typeName, _ := schema["type"].(string)
	var ok bool
	switch typeName {
	case "string":
		_, ok = value.(string)
	case "boolean":
		_, ok = value.(bool)
	case "number":
		ok = isNumber(value)
	case "integer":
		ok = isNumber(value) && isWhole(value)
	default:
		return fmt.Errorf("field %q has unsupported type %q", field, typeName)
	}
	if !ok {
		return fmt.Errorf("field %q must be a %s, got %T", field, typeName, value)
	}
	enum, hasEnum := schema["enum"].([]any)
	if !hasEnum {
		return nil
	}
	for _, allowed := range enum {
		if allowed == value {
			return nil
		}
	}
	return fmt.Errorf("field %q value %v is not one of %v", field, value, enum)
}

func isNumber(value any) bool {
	switch value.(type) {
	case float64, float32, int, int32, int64:
		return true
	}
	return false
}

func isWhole(value any) bool {
	switch number := value.(type) {
	case float64:
		return number == float64(int64(number))
	case float32:
		return number == float32(int64(number))
	}
	return true
}
