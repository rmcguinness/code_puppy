package app

import (
	"testing"

	"go.uber.org/goleak"
)

// No test may leave a goroutine running.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
