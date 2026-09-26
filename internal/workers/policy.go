package workers

import (
	"fmt"
	"slices"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
)

// Effective is what a worker may actually do and spend: its permissions
// without those the host policy doesn't allow, and its limits with the
// policy's defaults filled in and its maximums applied. Notes say what the
// policy changed.
type Effective struct {
	Permissions []Permission
	Limits      Limits
	Notes       []string
}

// Apply applies the host policy p to w.
func Apply(w *Worker, p config.WorkerPolicy) Effective {
	var out Effective
	for _, perm := range w.Permissions {
		if slices.Contains(p.Allow, perm.Kind) {
			out.Permissions = append(out.Permissions, perm)
		} else {
			out.Notes = append(out.Notes, fmt.Sprintf("the policy doesn't allow %s permissions: %s dropped", perm.Kind, perm))
		}
	}

	l := w.Limits
	if l.MaxTurns == 0 {
		l.MaxTurns = p.DefaultMaxTurns
	}
	if p.MaxTurns > 0 && l.MaxTurns > p.MaxTurns {
		out.Notes = append(out.Notes, fmt.Sprintf("max_turns %d capped at %d", l.MaxTurns, p.MaxTurns))
		l.MaxTurns = p.MaxTurns
	}
	if l.MaxCostUSD == 0 {
		l.MaxCostUSD = p.DefaultMaxCostUSD
	}
	if p.MaxCostUSD > 0 && l.MaxCostUSD > p.MaxCostUSD {
		out.Notes = append(out.Notes, fmt.Sprintf("max_cost_usd %.2f capped at %.2f", l.MaxCostUSD, p.MaxCostUSD))
		l.MaxCostUSD = p.MaxCostUSD
	}
	if l.Timeout == 0 {
		l.Timeout, _ = time.ParseDuration(p.DefaultTimeout)
	}
	if max, err := time.ParseDuration(p.MaxTimeout); err == nil && max > 0 && l.Timeout > max {
		out.Notes = append(out.Notes, fmt.Sprintf("timeout %s capped at %s", l.Timeout, max))
		l.Timeout = max
	}
	out.Limits = l
	return out
}
