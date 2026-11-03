package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// P-020: the helper must hard-fail when the database name lacks a _test suffix.
func TestAssertTestDatabase(t *testing.T) {
	valid := []string{
		"postgres://wallet:devonly@127.0.0.1:5433/check_wallet_test?sslmode=disable",
		"postgres://wallet:devonly@127.0.0.1:5433/scratch_test",
	}
	for _, dsn := range valid {
		assert.NoError(t, assertTestDatabase(dsn), dsn)
	}

	invalid := []string{
		"postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable",
		"postgres://wallet:devonly@127.0.0.1:5433/check_wallet_testing?sslmode=disable",
		"postgres://wallet:devonly@127.0.0.1:5433/postgres",
	}
	for _, dsn := range invalid {
		assert.Error(t, assertTestDatabase(dsn), dsn)
	}
}
