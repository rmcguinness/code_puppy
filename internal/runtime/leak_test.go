package runtime

import (
	"context"
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

// No test may leave a goroutine running. The shared test telemetry lives
// for the whole process, so it is shut down before the check.
func TestMain(m *testing.M) {
	code := m.Run()
	tel, _ := testTelemetry()
	_ = tel.Shutdown(context.Background())
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}
