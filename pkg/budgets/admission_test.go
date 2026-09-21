package budgets

import (
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdmit(t *testing.T) {
	sonnet := Rule{ID: uuid.New(), Name: "sonnet-hourly", Match: RuleMatch{Models: []string{"claude-sonnet-*"}}, Amount: 5, Window: "now-1h"}
	models := []api.Model{{Name: "claude-sonnet-5"}}
	var refusal *Refusal

	_, err := Admit([]Rule{sonnet}, nil, models, api.Budget{})
	require.True(t, errors.As(err, &refusal), "active rules without a per-run budget cost refuse: %v", err)

	_, err = Admit([]Rule{sonnet}, nil, append(models, api.Model{Name: "gpt-5"}), api.Budget{Cost: 1})
	require.True(t, errors.As(err, &refusal), "an uncovered fallback refuses: %v", err)

	admission, err := Admit([]Rule{sonnet}, nil, models, api.Budget{Cost: 1})
	require.NoError(t, err)
	require.Equal(t, 1.0, admission.Amount)

	admission, err = Admit(nil, nil, models, api.Budget{})
	require.NoError(t, err, "no rules means no budgeting")
	attributions, err := admission.Attributions(models[0])
	require.NoError(t, err)
	require.Empty(t, attributions)
}
