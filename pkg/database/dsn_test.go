package database

import "testing"

func TestMaskDSN(t *testing.T) {
	tests := map[string]string{
		"postgres://captain:secret@db.internal:5432/captain?sslmode=require": "postgres://captain:REDACTED@db.internal:5432/captain?sslmode=require",
		"postgres://db.internal/captain":                                     "postgres://db.internal/captain",
		"host=db.internal user=captain password=secret dbname=captain":       "host=db.internal user=captain password=REDACTED dbname=captain",
	}
	for input, want := range tests {
		if got := MaskDSN(input); got != want {
			t.Errorf("MaskDSN(%q) = %q, want %q", input, got, want)
		}
	}
}
