package api

// Memory controls which ambient context an agent loads. The zero value loads
// everything (default agent behaviour); each Skip* strips one source.
// Consolidates the legacy ai.Request.{SkillDirs,NoSkills,NoProject,NoUser,
// NoMemory,NoHooks,Bare} toggles.
type Memory struct {
	// Skills are extra skill/plugin directories to load. (ai.Request.SkillDirs)
	Skills []string `json:"skills,omitempty" yaml:"skills,omitempty" pretty:"label=Skill Dirs"`
	// SkipProject drops project/local settings. (ai.Request.NoProject)
	SkipProject bool `json:"skipProject,omitempty" yaml:"skipProject,omitempty" pretty:"label=Skip Project"`
	// SkipUser drops ~/.claude user settings. (ai.Request.NoUser)
	SkipUser bool `json:"skipUser,omitempty" yaml:"skipUser,omitempty" pretty:"label=Skip User"`
	// SkipSkills disables slash commands/skills. (ai.Request.NoSkills)
	SkipSkills bool `json:"skipSkills,omitempty" yaml:"skipSkills,omitempty" pretty:"label=Skip Skills"`
	// SkipHooks skips hooks. (ai.Request.NoHooks)
	SkipHooks bool `json:"skipHooks,omitempty" yaml:"skipHooks,omitempty" pretty:"label=Skip Hooks"`
	// SkipMemory skips auto-memory and CLAUDE.md. (ai.Request.NoMemory)
	SkipMemory bool `json:"skipMemory,omitempty" yaml:"skipMemory,omitempty" pretty:"label=Skip Memory"`
	// Bare skips hooks, skills, memory, and ambient settings at once. (ai.Request.Bare)
	Bare bool `json:"bare,omitempty" yaml:"bare,omitempty" pretty:"label=Bare"`
}
