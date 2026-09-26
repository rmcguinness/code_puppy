package skills

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/config"
)

// Evaluation is what a skill may do under a host policy: for each setting,
// the stricter of what the skill asks for and what the policy allows.
type Evaluation struct {
	Tier   HITLTier // approval tier its scripts run at
	Bypass bool     // Tier0BypassAll granted: the skill asked and the policy allows it
	// Network is granted: the skill asked (custom_hints.network) and is on
	// the policy's allowlist.
	Network bool
	// Env are the host environment variables the skill asked for that the
	// policy passes through; Withheld are the ones it doesn't.
	Env, Withheld []string
	Hash          string // ContentHash, "" if it couldn't be computed
	Scripts       []ScriptVerdict
	// Blocked are reasons none of the skill's scripts may run.
	Blocked []string
}

// ScriptVerdict is whether one script may run, and with what limits.
type ScriptVerdict struct {
	Name           string
	Allowed        bool
	Reasons        []string // why it may not run
	TimeoutSeconds int      // capped by the policy
	Dependencies   []string
}

// Runnable reports whether any script may run.
func (e Evaluation) Runnable() bool {
	for _, s := range e.Scripts {
		if s.Allowed {
			return true
		}
	}
	return false
}

// Evaluate applies policy p to skill s.
func Evaluate(s *Skill, p config.SkillPolicy) Evaluation {
	var ev Evaluation
	block := func(format string, args ...any) { ev.Blocked = append(ev.Blocked, fmt.Sprintf(format, args...)) }

	for _, prob := range s.Problems {
		block("definition: %s", prob)
	}
	if h, err := s.ContentHash(); err == nil {
		ev.Hash = h
	} else if len(s.Scripts) > 0 {
		block("content can't be hashed: %v", err)
	}
	if len(p.TrustedHashes) > 0 && !slices.Contains(p.TrustedHashes, ev.Hash) {
		block("its content (%s) isn't in skills.policy.trusted_hashes", orNone(ev.Hash))
	}
	for _, t := range s.ToolRequirements {
		scopes := t.Scopes
		if len(scopes) == 0 {
			scopes = []string{""}
		}
		for _, sc := range scopes {
			ref := t.Name
			if sc != "" {
				ref += ":" + sc
			}
			if g := matchAny(p.DenyTools, ref); g != "" {
				block("needs %s, denied by skills.policy.deny_tools (%s)", ref, g)
			}
		}
	}

	ev.Tier, ev.Bypass = effectiveTier(s, p)

	hints := s.ExecutionHints
	if hints.NeedsNetwork() {
		if strings.EqualFold(p.Network, "allowlist") && slices.Contains(p.NetworkAllow, s.Name) {
			ev.Network = true
		} else {
			block("needs the network, which skills.policy doesn't allow it (network_allow)")
		}
	}
	if hints != nil {
		for _, name := range hints.EnvironmentVariables {
			if matchAny(p.EnvPassthrough, name) != "" {
				ev.Env = append(ev.Env, name)
			} else {
				ev.Withheld = append(ev.Withheld, name)
			}
		}
	}

	for _, sc := range s.Scripts {
		v := ScriptVerdict{Name: sc.Name, Dependencies: sc.Dependencies, Reasons: slices.Clone(ev.Blocked)}
		if !slices.ContainsFunc(p.Languages, func(l string) bool { return strings.EqualFold(l, string(sc.Language)) }) {
			v.Reasons = append(v.Reasons, fmt.Sprintf("language %q isn't in skills.policy.languages", orNone(string(sc.Language))))
		}
		v.TimeoutSeconds = sc.TimeoutSeconds
		if v.TimeoutSeconds == 0 && hints != nil {
			v.TimeoutSeconds = hints.TimeoutSeconds
		}
		if p.MaxTimeoutSeconds > 0 && (v.TimeoutSeconds == 0 || v.TimeoutSeconds > p.MaxTimeoutSeconds) {
			v.TimeoutSeconds = p.MaxTimeoutSeconds
		}
		for _, dep := range sc.Dependencies {
			if r := checkDependency(dep, p.Packages); r != "" {
				v.Reasons = append(v.Reasons, r)
			}
		}
		v.Allowed = len(v.Reasons) == 0
		ev.Scripts = append(ev.Scripts, v)
	}
	return ev
}

// effectiveTier is the stricter of the skill's declared tier and the
// policy's minimum. Tier 0 (no approval) needs both the skill and the
// policy to allow a bypass; otherwise it becomes tier 3.
func effectiveTier(s *Skill, p config.SkillPolicy) (HITLTier, bool) {
	declared := s.DeclaredTier()
	if declared == Tier0BypassAll {
		if p.AllowHITLBypass && s.ExecutionHints != nil && s.ExecutionHints.AllowHITLBypass {
			return Tier0BypassAll, true
		}
		return Tier3MandatoryApproval, false
	}
	floor := Tier1AutoRead + HITLTier(max(p.MinHITLTier-1, 0)) // min_hitl_tier 0 or 1 -> tier 1
	floor = min(floor, Tier3MandatoryApproval)
	return max(declared, floor), false
}

// requirementRE is a plain PEP 508 requirement: a name, optional extras,
// and version specifiers. URLs, paths and pip options are not.
var requirementRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(\[[A-Za-z0-9._,\s-]+\])?\s*((?:[<>=!~]=?|===)\s*[A-Za-z0-9.*+!_-]+(?:\s*,\s*(?:[<>=!~]=?|===)\s*[A-Za-z0-9.*+!_-]+)*)?\s*(;.*)?$`)

// checkDependency returns why dep isn't allowed, or "".
func checkDependency(dep string, p config.PackagePolicy) string {
	m := requirementRE.FindStringSubmatch(strings.TrimSpace(dep))
	if m == nil {
		return fmt.Sprintf("dependency %q isn't a plain package requirement (URLs, paths and pip options aren't allowed)", dep)
	}
	name := normalizePackage(m[1])
	if g := matchAny(normalizeAll(p.Deny), name); g != "" {
		return fmt.Sprintf("dependency %s is denied by skills.policy.packages.deny (%s)", name, g)
	}
	if len(p.Allow) > 0 && matchAny(normalizeAll(p.Allow), name) == "" {
		return fmt.Sprintf("dependency %s isn't in skills.policy.packages.allow", name)
	}
	if p.RequireHashes && !exactPin(m[3]) {
		return fmt.Sprintf("dependency %q must be pinned with == (skills.policy.packages.require_hashes)", dep)
	}
	return ""
}

func exactPin(spec string) bool {
	spec = strings.TrimSpace(spec)
	return strings.HasPrefix(spec, "==") && !strings.Contains(spec, ",") && !strings.Contains(spec, "*")
}

var packageSepRE = regexp.MustCompile(`[-_.]+`)

// normalizePackage is a package name's canonical form (PEP 503).
func normalizePackage(name string) string {
	return packageSepRE.ReplaceAllString(strings.ToLower(name), "-")
}

func normalizeAll(globs []string) []string {
	out := make([]string, len(globs))
	for i, g := range globs {
		out[i] = normalizePackage(g)
	}
	return out
}

// matchAny returns the first glob that matches s (case-insensitively), or "".
func matchAny(globs []string, s string) string {
	for _, g := range globs {
		if ok, _ := path.Match(strings.ToLower(g), strings.ToLower(s)); ok {
			return g
		}
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
