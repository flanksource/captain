package database

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNormalizePlanMarkdownProducesStableHash(t *testing.T) {
	left := normalizePlanMarkdown("\r\n# Plan\r\n\r\nstep\r\n")
	right := normalizePlanMarkdown("# Plan\n\nstep\n")
	assert.Equal(t, right, left)
	assert.Equal(t, planContentHash(right), planContentHash(left))
}

func TestPlanStoreRejectsInvalidInputBeforeDatabaseAccess(t *testing.T) {
	db := &DB{gorm: nil}
	_, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{PlanID: uuid.New(), PlanMarkdown: "x"})
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrPlanNotFound))
}
