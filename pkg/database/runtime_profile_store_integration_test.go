package database

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openRuntimeDB(t *testing.T, name string) *DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: name})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// presetSpecWithCustomMarshalers exercises the two Spec fragments whose JSON
// shape is produced by hand-written marshalers (Tools and MCP), so a jsonb
// round trip that dropped or reshaped them would be caught here.
func presetSpecWithCustomMarshalers() api.RuntimePresetSpec {
	return api.RuntimePresetSpec{
		Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeCLI},
		Permissions: api.Permissions{
			Mode:  api.PermissionPlan,
			Tools: api.Tools{"Bash": api.ToolPolicyAllow, "Write": api.ToolPolicyDeny},
			MCP: api.MCP{
				Servers: []string{"github"},
				Modes:   api.ResourcePolicies{"jira": api.ResourceDisabled},
			},
		},
	}
}

func TestRuntimePresetStoreRoundTripsSpecThroughJSONB(t *testing.T) {
	db := openRuntimeDB(t, "captain_runtime_preset_roundtrip")

	created, err := db.CreateRuntimePreset(t.Context(), RuntimePresetInput{
		Name: "Personal", Description: "my defaults", Scope: api.SpecLayerUser,
		Spec: presetSpecWithCustomMarshalers(),
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, created.ID)
	assert.Equal(t, presetSpecWithCustomMarshalers(), created.Spec)

	stored, err := db.GetRuntimePreset(t.Context(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, *created, *stored)
	assert.Equal(t, presetSpecWithCustomMarshalers(), stored.Spec)
	assert.Equal(t, "my defaults", stored.Description)
	assert.Equal(t, api.SpecLayerUser, stored.Scope)
	assert.False(t, stored.CreatedAt.IsZero())
	assert.False(t, stored.UpdatedAt.IsZero())
}

func TestRuntimePresetStoreCRUD(t *testing.T) {
	db := openRuntimeDB(t, "captain_runtime_preset_crud")

	_, err := db.GetRuntimePreset(t.Context(), uuid.New())
	assert.ErrorIs(t, err, ErrRuntimePresetNotFound)
	_, err = db.FindRuntimePresetByName(t.Context(), "nobody")
	assert.ErrorIs(t, err, ErrRuntimePresetNotFound)

	personal, err := db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "  Personal ", Scope: api.SpecLayerUser})
	require.NoError(t, err)
	assert.Equal(t, "Personal", personal.Name, "the stored name is trimmed")
	org, err := db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "Organization", Scope: api.SpecLayerGlobal})
	require.NoError(t, err)

	byName, err := db.FindRuntimePresetByName(t.Context(), "pErSoNaL")
	require.NoError(t, err)
	assert.Equal(t, personal.ID, byName.ID, "lookup by name is case-insensitive")

	listed, err := db.ListRuntimePresets(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, []string{"Organization", "Personal"}, []string{listed[0].Name, listed[1].Name}, "listed by name")

	updated, err := db.UpdateRuntimePreset(t.Context(), personal.ID, RuntimePresetInput{
		Name: "Personal", Description: "renamed later", Scope: api.SpecLayerContext,
		Spec: api.RuntimePresetSpec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}},
	})
	require.NoError(t, err)
	assert.Equal(t, personal.ID, updated.ID)
	assert.Equal(t, api.SpecLayerContext, updated.Scope)
	assert.Equal(t, "renamed later", updated.Description)
	assert.Equal(t, "gpt-5", updated.Spec.Name)
	assert.Equal(t, personal.CreatedAt, updated.CreatedAt, "an update keeps created_at")
	assert.True(t, updated.UpdatedAt.After(personal.UpdatedAt), "an update bumps updated_at")

	_, err = db.UpdateRuntimePreset(t.Context(), uuid.New(), RuntimePresetInput{Name: "ghost", Scope: api.SpecLayerUser})
	assert.ErrorIs(t, err, ErrRuntimePresetNotFound)

	require.NoError(t, db.DeleteRuntimePreset(t.Context(), org.ID))
	_, err = db.GetRuntimePreset(t.Context(), org.ID)
	assert.ErrorIs(t, err, ErrRuntimePresetNotFound)
	assert.ErrorIs(t, db.DeleteRuntimePreset(t.Context(), org.ID), ErrRuntimePresetNotFound)

	remaining, err := db.ListRuntimePresets(t.Context())
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, personal.ID, remaining[0].ID)
}

func TestRuntimePresetStoreRejectsInvalidInputBeforeWriting(t *testing.T) {
	db := openRuntimeDB(t, "captain_runtime_preset_invalid")

	_, err := db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "   ", Scope: api.SpecLayerUser})
	assert.ErrorIs(t, err, ErrRuntimeInvalid)

	_, err = db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "Bad scope", Scope: "team"})
	assert.ErrorIs(t, err, ErrRuntimeInvalid)
	assert.Contains(t, err.Error(), `scope "team"`)

	_, err = db.CreateRuntimePreset(t.Context(), RuntimePresetInput{
		Name: "Bad model", Scope: api.SpecLayerUser,
		Spec: api.RuntimePresetSpec{Model: api.Model{Name: "gpt-5", Mode: "telepathy"}},
	})
	assert.ErrorIs(t, err, ErrRuntimeInvalid)

	listed, err := db.ListRuntimePresets(t.Context())
	require.NoError(t, err)
	assert.Empty(t, listed, "nothing invalid reached the table")
	assert.NotNil(t, listed, "an empty result must marshal as [] rather than null")
}

func TestRuntimePresetStoreEnforcesCaseInsensitiveNames(t *testing.T) {
	db := openRuntimeDB(t, "captain_runtime_preset_names")

	_, err := db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "Personal", Scope: api.SpecLayerUser})
	require.NoError(t, err)
	other, err := db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "Other", Scope: api.SpecLayerUser})
	require.NoError(t, err)

	_, err = db.CreateRuntimePreset(t.Context(), RuntimePresetInput{Name: "personal", Scope: api.SpecLayerUser})
	assert.ErrorIs(t, err, ErrRuntimeNameTaken)

	_, err = db.UpdateRuntimePreset(t.Context(), other.ID, RuntimePresetInput{Name: "PERSONAL", Scope: api.SpecLayerUser})
	assert.ErrorIs(t, err, ErrRuntimeNameTaken)

	same, err := db.UpdateRuntimePreset(t.Context(), other.ID, RuntimePresetInput{Name: "other", Scope: api.SpecLayerUser})
	require.NoError(t, err, "a record may change the case of its own name")
	assert.Equal(t, "other", same.Name)
}

func TestRuntimeProfileStoreCRUD(t *testing.T) {
	db := openRuntimeDB(t, "captain_runtime_profile_crud")

	_, err := db.GetRuntimeProfile(t.Context(), uuid.New())
	assert.ErrorIs(t, err, ErrRuntimeProfileNotFound)
	_, err = db.FindRuntimeProfileByName(t.Context(), "nobody")
	assert.ErrorIs(t, err, ErrRuntimeProfileNotFound)

	spec := api.Spec{
		Model:       api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeCLI},
		Permissions: api.Permissions{Mode: api.PermissionPlan, Tools: api.Tools{"Bash": api.ToolPolicyAsk}},
	}
	presetID := uuid.New().String()
	review, err := db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{
		Name: "Review", Description: "code review", Spec: spec, Presets: []string{"Personal", presetID},
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, review.ID)
	assert.Equal(t, spec, review.Spec)
	assert.Equal(t, []string{"Personal", presetID}, review.Presets)

	stored, err := db.GetRuntimeProfile(t.Context(), review.ID)
	require.NoError(t, err)
	assert.Equal(t, *review, *stored)

	byName, err := db.FindRuntimeProfileByName(t.Context(), "REVIEW")
	require.NoError(t, err)
	assert.Equal(t, review.ID, byName.ID)

	empty, err := db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{Name: "Empty"})
	require.NoError(t, err)
	assert.Empty(t, empty.Presets)

	listed, err := db.ListRuntimeProfiles(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, []string{"Empty", "Review"}, []string{listed[0].Name, listed[1].Name})

	updated, err := db.UpdateRuntimeProfile(t.Context(), review.ID, RuntimeProfileInput{
		Name: "Review", Spec: api.Spec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}}, Presets: []string{"Organization"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Organization"}, updated.Presets)
	assert.Equal(t, "gpt-5", updated.Spec.Name)
	assert.Empty(t, updated.Description, "an update replaces every mutable field")
	assert.Equal(t, review.CreatedAt, updated.CreatedAt)
	assert.True(t, updated.UpdatedAt.After(review.UpdatedAt))

	_, err = db.UpdateRuntimeProfile(t.Context(), uuid.New(), RuntimeProfileInput{Name: "ghost"})
	assert.ErrorIs(t, err, ErrRuntimeProfileNotFound)

	require.NoError(t, db.DeleteRuntimeProfile(t.Context(), empty.ID))
	assert.ErrorIs(t, db.DeleteRuntimeProfile(t.Context(), empty.ID), ErrRuntimeProfileNotFound)
}

func TestRuntimeProfileStoreRejectsInvalidInputAndDuplicateNames(t *testing.T) {
	db := openRuntimeDB(t, "captain_runtime_profile_invalid")

	_, err := db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{Name: ""})
	assert.ErrorIs(t, err, ErrRuntimeInvalid)

	_, err = db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{Name: "Blank ref", Presets: []string{"Personal", "  "}})
	assert.ErrorIs(t, err, ErrRuntimeInvalid)

	review, err := db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{Name: "Review"})
	require.NoError(t, err)
	_, err = db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{Name: "review"})
	assert.ErrorIs(t, err, ErrRuntimeNameTaken)

	plan, err := db.CreateRuntimeProfile(t.Context(), RuntimeProfileInput{Name: "Plan"})
	require.NoError(t, err)
	_, err = db.UpdateRuntimeProfile(t.Context(), plan.ID, RuntimeProfileInput{Name: "REVIEW"})
	assert.ErrorIs(t, err, ErrRuntimeNameTaken)

	kept, err := db.GetRuntimeProfile(t.Context(), review.ID)
	require.NoError(t, err)
	assert.Equal(t, "Review", kept.Name)
}
