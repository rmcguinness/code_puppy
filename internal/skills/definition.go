package skills

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// The types below mirror castor.skills.v1 (proto/castor/skills/v1/skill.proto
// in github.com/retail-cortex/castor, commit 1ce880f5), using the proto
// field names as YAML keys in SKILL.md frontmatter. Castor publishes only
// the .proto source, so they are written out here rather than imported.
// Resources, scenarios and the registry-only fields (references, examples,
// compiled schemas) aren't read yet.

// AuthorDetails is a skill contributor.
type AuthorDetails struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email,omitempty"`
	URL   string `yaml:"url,omitempty"`
}

// ToolRequirement is a tool a skill needs, with the scopes it needs it
// for (e.g. Bash with ["git:*"]).
type ToolRequirement struct {
	Name        string   `yaml:"name"`
	Scopes      []string `yaml:"scopes,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// HITLTier is a human-in-the-loop risk tier: how much approval a skill's
// actions need. Values match the proto enum, so the zero value is
// TierUnspecified (never a bypass) and higher tiers compare greater.
type HITLTier int

const (
	TierUnspecified HITLTier = iota
	Tier0BypassAll
	Tier1AutoRead
	Tier2AuditedWrite
	Tier3MandatoryApproval
)

var tierNames = map[HITLTier]string{
	Tier0BypassAll:         "TIER_0_BYPASS_ALL",
	Tier1AutoRead:          "TIER_1_AUTO_READ",
	Tier2AuditedWrite:      "TIER_2_AUDITED_WRITE",
	Tier3MandatoryApproval: "TIER_3_MANDATORY_APPROVAL",
}

func (t HITLTier) String() string {
	if n, ok := tierNames[t]; ok {
		return n
	}
	return "TIER_UNSPECIFIED"
}

// ParseHITLTier reads a tier by name: the proto enum name
// (HITL_POLICY_TIER_2_AUDITED_WRITE), the short form Castor stores
// (TIER_2_AUDITED_WRITE), or tier_2. Bare numbers are refused: the proto
// numbers its enum one higher than the tier, so "2" would be ambiguous.
func ParseHITLTier(s string) (HITLTier, error) {
	n := strings.ToUpper(strings.TrimSpace(s))
	n = strings.TrimPrefix(n, "HITL_POLICY_")
	switch n {
	case "", "TIER_UNSPECIFIED", "UNSPECIFIED":
		return TierUnspecified, nil
	}
	for t, name := range tierNames {
		if n == name || n == name[:6] { // "TIER_2"
			return t, nil
		}
	}
	return TierUnspecified, fmt.Errorf("unknown HITL tier %q (use e.g. TIER_2_AUDITED_WRITE)", s)
}

// UnmarshalYAML reads a tier name (see ParseHITLTier).
func (t *HITLTier) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!int" {
		return fmt.Errorf("line %d: HITL tier must be a name such as TIER_2_AUDITED_WRITE, not a number", n.Line)
	}
	v, err := ParseHITLTier(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*t = v
	return nil
}

// ExecutionHints are operational hints for running a skill.
type ExecutionHints struct {
	PreferredModel        string            `yaml:"preferred_model,omitempty"`
	RequiresHumanApproval bool              `yaml:"requires_human_approval,omitempty"`
	EnvironmentVariables  []string          `yaml:"environment_variables,omitempty"`
	TimeoutSeconds        int               `yaml:"timeout_seconds,omitempty"`
	CustomHints           map[string]string `yaml:"custom_hints,omitempty"`
	HITLTier              HITLTier          `yaml:"hitl_tier,omitempty"`
	AllowHITLBypass       bool              `yaml:"allow_hitl_bypass,omitempty"`
}

// NeedsNetwork reports whether the skill's scripts declare that they need
// the network. The proto has no field for it yet, so it is the custom
// hint network: "true" (or "required").
func (h *ExecutionHints) NeedsNetwork() bool {
	if h == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(h.CustomHints["network"])) {
	case "true", "yes", "required":
		return true
	}
	return false
}

// ScriptLanguage is a script's runtime.
type ScriptLanguage string

const (
	LanguageUnspecified ScriptLanguage = ""
	LanguagePython      ScriptLanguage = "python"
	LanguageTypeScript  ScriptLanguage = "typescript"
)

// UnmarshalYAML reads python / typescript, or the proto enum names.
func (l *ScriptLanguage) UnmarshalYAML(n *yaml.Node) error {
	v := strings.ToLower(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(n.Value)), "SCRIPT_LANGUAGE_"))
	switch v {
	case "", "unspecified":
		*l = LanguageUnspecified
	case "python", "py":
		*l = LanguagePython
	case "typescript", "ts":
		*l = LanguageTypeScript
	default:
		return fmt.Errorf("line %d: unknown script language %q (use python or typescript)", n.Line, n.Value)
	}
	return nil
}

// ScriptDefinition is a helper script shipped with (or referenced by) a skill.
// Exactly one of InlineCode, StorageURI and RelativePath is its source.
type ScriptDefinition struct {
	Name                 string            `yaml:"name"`
	Description          string            `yaml:"description,omitempty"`
	Language             ScriptLanguage    `yaml:"language,omitempty"`
	InlineCode           string            `yaml:"inline_code,omitempty"`
	StorageURI           string            `yaml:"storage_uri,omitempty"`
	RelativePath         string            `yaml:"relative_path,omitempty"`
	EntryPoint           string            `yaml:"entry_point,omitempty"`
	Dependencies         []string          `yaml:"dependencies,omitempty"`
	TimeoutSeconds       int               `yaml:"timeout_seconds,omitempty"`
	EnvironmentVariables map[string]string `yaml:"environment_variables,omitempty"`
}

// CompiledReference is Castor's pointer to a registered, hashed skill.
// Only its hash and tier are read.
type CompiledReference struct {
	SkillID    string   `yaml:"skill_id,omitempty"`
	SHA256Hash string   `yaml:"sha256_hash,omitempty"`
	HITLTier   HITLTier `yaml:"hitl_tier,omitempty"`
}

var (
	envNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	scriptNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Validate reports problems with the skill's definition. A skill with
// problems still loads (its instructions are useful), but the scripts
// involved are not run.
func (m *SkillMetadata) Validate() []string {
	var probs []string
	seen := map[string]bool{}
	for i, s := range m.Scripts {
		label := fmt.Sprintf("scripts[%d]", i)
		if s.Name != "" {
			label = fmt.Sprintf("script %q", s.Name)
		}
		switch {
		case s.Name == "":
			probs = append(probs, label+": needs a name")
		case !scriptNameRE.MatchString(s.Name):
			probs = append(probs, label+": name may only use letters, digits, '.', '_' and '-'")
		case seen[s.Name]:
			probs = append(probs, label+": name is used twice")
		}
		seen[s.Name] = true
		sources := 0
		for _, v := range []string{s.InlineCode, s.StorageURI, s.RelativePath} {
			if v != "" {
				sources++
			}
		}
		if sources != 1 {
			probs = append(probs, label+": needs exactly one of inline_code, storage_uri or relative_path")
		}
		if s.RelativePath != "" && !inBundle(s.RelativePath) {
			probs = append(probs, label+": relative_path must stay inside the skill's directory")
		}
		if s.StorageURI != "" {
			probs = append(probs, label+": storage_uri isn't supported yet")
		}
		if s.Language == LanguageUnspecified {
			probs = append(probs, label+": needs a language")
		}
		if s.TimeoutSeconds < 0 {
			probs = append(probs, label+": timeout_seconds can't be negative")
		}
		for k := range s.EnvironmentVariables {
			if !envNameRE.MatchString(k) {
				probs = append(probs, fmt.Sprintf("%s: invalid environment variable name %q", label, k))
			}
		}
	}
	if h := m.ExecutionHints; h != nil {
		for _, k := range h.EnvironmentVariables {
			if !envNameRE.MatchString(k) {
				probs = append(probs, fmt.Sprintf("execution_hints: invalid environment variable name %q", k))
			}
		}
		if h.TimeoutSeconds < 0 {
			probs = append(probs, "execution_hints: timeout_seconds can't be negative")
		}
	}
	for i, t := range m.ToolRequirements {
		if strings.TrimSpace(t.Name) == "" {
			probs = append(probs, fmt.Sprintf("tool_requirements[%d]: needs a name", i))
		}
	}
	return probs
}

// inBundle reports whether rel is a relative path that stays inside the
// skill's directory.
func inBundle(rel string) bool {
	if strings.Contains(rel, `\`) || path.IsAbs(rel) {
		return false
	}
	c := path.Clean(rel)
	return c != "." && c != ".." && !strings.HasPrefix(c, "../")
}

// DeclaredTier is the tier the skill asks for: execution_hints.hitl_tier,
// else the compiled reference's, and at least tier 3 when it says it
// requires human approval.
func (m *SkillMetadata) DeclaredTier() HITLTier {
	t := TierUnspecified
	if m.ExecutionHints != nil {
		t = m.ExecutionHints.HITLTier
	}
	if t == TierUnspecified && m.CompiledReference != nil {
		t = m.CompiledReference.HITLTier
	}
	if m.ExecutionHints != nil && m.ExecutionHints.RequiresHumanApproval {
		t = Tier3MandatoryApproval
	}
	return t
}

// AllowedToolsList is the spec's allowed-tools (or Castor's legacy
// allowed_tools), split on spaces and commas.
func (m *SkillMetadata) AllowedToolsList() []string {
	s := m.AllowedTools
	if s == "" {
		s = m.AllowedToolsLegacy
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' })
}
