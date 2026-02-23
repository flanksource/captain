package ai

import "errors"

var (
	ErrBudgetExceeded    = errors.New("budget exceeded")
	ErrCLINotFound       = errors.New("CLI tool not found")
	ErrCLIExecutionFailed = errors.New("CLI execution failed")
	ErrTimeout           = errors.New("operation timed out")
	ErrSchemaValidation  = errors.New("schema validation failed")
	ErrModelNotFound     = errors.New("model not found in pricing registry")
	ErrNoAPIKey          = errors.New("API key not found")
)
