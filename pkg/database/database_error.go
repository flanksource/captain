package database

import (
	"errors"

	"gorm.io/gorm"
)

type sqlStateError interface {
	SQLState() string
}

func isUniqueViolation(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var sqlState sqlStateError
	return errors.As(err, &sqlState) && sqlState.SQLState() == "23505"
}
