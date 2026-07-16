package api

// Merge returns a copy of s with override's set (non-zero) fields taking
// precedence. A zero-valued field in override is treated as "unset" and keeps
// s's value, so a base spec can supply defaults that an operation-specific spec
// selectively overrides:
//
//	resolved := base.Merge(operation)
//
// Scalar fields (model name, effort, budget cost, prompt user, …) merge
// individually. Slices, maps, and pointers (Fallbacks, Metadata, Setup,
// Workflow, CLIArgs, Permissions sub-values) replace wholesale when set in
// override rather than deep-merging — an override that lists tools means exactly
// those tools. Boolean toggles (NoCache, Skip*) follow zero=unset: an override
// can turn a flag on but not off, since false is indistinguishable from absent.
func (s Spec) Merge(override Spec) Spec {
	s.Model = s.merge(override.Model)
	s.Prompt = s.Prompt.merge(override.Prompt)
	s.Budget = s.Budget.merge(override.Budget)
	s.Memory = s.Memory.merge(override.Memory)
	s.Permissions = s.Permissions.merge(override.Permissions)
	if override.Setup != nil {
		s.Setup = override.Setup
	}
	if override.Workflow != nil {
		s.Workflow = override.Workflow
	}
	if override.SessionID != "" {
		s.SessionID = override.SessionID
	}
	if len(override.CLIArgs) > 0 {
		s.CLIArgs = override.CLIArgs
	}
	return s
}

func (m Model) merge(o Model) Model {
	if o.Name != "" {
		m.Name = o.Name
	}
	if o.ID != "" {
		m.ID = o.ID
	}
	if o.Backend != "" {
		m.Backend = o.Backend
	}
	if o.Temperature != nil {
		m.Temperature = o.Temperature
	}
	if o.Effort != "" {
		m.Effort = o.Effort
	}
	if o.NoCache {
		m.NoCache = true
	}
	if len(o.Fallbacks) > 0 {
		m.Fallbacks = o.Fallbacks
	}
	return m
}

func (p Prompt) merge(o Prompt) Prompt {
	if o.User != "" {
		p.User = o.User
	}
	if o.System != "" {
		p.System = o.System
	}
	if o.AppendSystem != "" {
		p.AppendSystem = o.AppendSystem
	}
	if o.Source != "" {
		p.Source = o.Source
	}
	if o.Schema != nil {
		p.Schema = o.Schema
	}
	if len(o.SchemaJSON) > 0 {
		p.SchemaJSON = o.SchemaJSON
	}
	if o.SchemaStrictness != "" {
		p.SchemaStrictness = o.SchemaStrictness
	}
	if len(o.Metadata) > 0 {
		p.Metadata = o.Metadata
	}
	return p
}

func (b Budget) merge(o Budget) Budget {
	if o.Cost != 0 {
		b.Cost = o.Cost
	}
	if o.MaxTokens != 0 {
		b.MaxTokens = o.MaxTokens
	}
	if o.MaxTurns != 0 {
		b.MaxTurns = o.MaxTurns
	}
	if o.Timeout != "" {
		b.Timeout = o.Timeout
	}
	return b
}

func (m Memory) merge(o Memory) Memory {
	if len(o.Skills) > 0 {
		m.Skills = o.Skills
	}
	if o.SkipProject {
		m.SkipProject = true
	}
	if o.SkipUser {
		m.SkipUser = true
	}
	if o.SkipSkills {
		m.SkipSkills = true
	}
	if o.SkipHooks {
		m.SkipHooks = true
	}
	if o.SkipMemory {
		m.SkipMemory = true
	}
	if o.Bare {
		m.Bare = true
	}
	return m
}

func (p Permissions) merge(o Permissions) Permissions {
	if o.Mode != "" {
		p.Mode = o.Mode
	}
	if len(o.Presets) > 0 {
		p.Presets = o.Presets
	}
	if len(o.Tools.Allow) > 0 || len(o.Tools.Deny) > 0 || len(o.Tools.Modes) > 0 {
		p.Tools = o.Tools
	}
	if o.MCP.Disabled || len(o.MCP.Servers) > 0 || len(o.MCP.Modes) > 0 {
		p.MCP = o.MCP
	}
	if len(o.Plugins) > 0 {
		p.Plugins = o.Plugins
	}
	if len(o.Skills) > 0 {
		p.Skills = o.Skills
	}
	return p
}
