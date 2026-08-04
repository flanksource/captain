package sandbox

import "testing"

func TestPlaceholderEnvNeverLeaksValues(t *testing.T) {
	result := TokenResult{
		Provider:    "github",
		EnvVars:     map[string]string{"GITHUB_TOKEN": "ghp_real", "GH_TOKEN": "ghp_real"},
		Placeholder: "captain-placeholder-gh-authorization",
	}
	env := result.PlaceholderEnv()
	if len(env) != 2 {
		t.Fatalf("env = %v", env)
	}
	for k, v := range env {
		if v != result.Placeholder {
			t.Fatalf("%s = %q, want the placeholder", k, v)
		}
	}

	// Without a placeholder there is no safe environment to hand out.
	if env := (TokenResult{EnvVars: map[string]string{"X": "y"}}).PlaceholderEnv(); env != nil {
		t.Fatalf("env = %v, want nil so callers cannot fall back to real values", env)
	}
}
