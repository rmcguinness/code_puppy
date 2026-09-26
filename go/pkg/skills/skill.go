package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillMetadata defines metadata stored in SKILL.md frontmatter: the Agent
// Skills spec fields, plus Castor's skill definition fields (see
// definition.go). Unknown keys are ignored, so skills written for other
// tools still load.
type SkillMetadata struct {
	// Agent Skills spec.
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`

	// Castor (castor.skills.v1.SkillDefinition).
	Tags               []string           `yaml:"tags"`
	Version            string             `yaml:"version,omitempty"`
	Author             string             `yaml:"author,omitempty"`
	Authors            []AuthorDetails    `yaml:"authors,omitempty"`
	AllowedToolsLegacy string             `yaml:"allowed_tools,omitempty"`
	ToolRequirements   []ToolRequirement  `yaml:"tool_requirements,omitempty"`
	Category           string             `yaml:"category,omitempty"`
	TriggerPhrases     []string           `yaml:"trigger_phrases,omitempty"`
	ExecutionHints     *ExecutionHints    `yaml:"execution_hints,omitempty"`
	CompiledReference  *CompiledReference `yaml:"compiled_reference,omitempty"`
	Scripts            []ScriptDefinition `yaml:"scripts,omitempty"`
	SkillID            string             `yaml:"skill_id,omitempty"`
	URI                string             `yaml:"uri,omitempty"`
	SourceURI          string             `yaml:"source_uri,omitempty"`
}

// Skill represents an Agent Skill specification.
type Skill struct {
	SkillMetadata
	Content   string   `json:"content"`
	Path      string   `json:"path"`
	Resources []string `json:"resources"`
	// Problems are definition errors (see SkillMetadata.Validate).
	Problems []string `json:"problems,omitempty"`

	fsys    fs.FS  // the skill's files, for ContentHash
	root    string // the skill's directory within fsys
	hostDir string // the skill's directory on disk; "" for built-in skills
}

// ParseSkillMD parses a SKILL.md document with YAML frontmatter.
func ParseSkillMD(content []byte, path string) (*Skill, error) {
	trimmed := bytes.TrimSpace(content)
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, fmt.Errorf("SKILL.md missing starting frontmatter delimiter '---'")
	}

	rest := trimmed[3:]
	idx := bytes.Index(rest, []byte("---"))
	if idx == -1 {
		return nil, fmt.Errorf("SKILL.md missing closing frontmatter delimiter '---'")
	}

	frontmatterBytes := rest[:idx]
	instructionBytes := bytes.TrimSpace(rest[idx+3:])

	var meta SkillMetadata
	if err := yaml.Unmarshal(frontmatterBytes, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse SKILL.md frontmatter: %w", err)
	}

	if meta.Name == "" {
		return nil, fmt.Errorf("SKILL.md must specify a name")
	}

	return &Skill{
		SkillMetadata: meta,
		Content:       string(instructionBytes),
		Path:          path,
		Resources:     []string{},
		Problems:      meta.Validate(),
	}, nil
}

// Matches returns true if the query matches the skill's name, description, or tags.
func (s *Skill) Matches(query string) bool {
	q := strings.ToLower(query)
	if strings.Contains(strings.ToLower(s.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(s.Description), q) {
		return true
	}
	for _, tag := range s.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

// maxHashedBytes bounds how much of a skill's directory ContentHash reads.
const maxHashedBytes = 64 << 20

// ContentHash is the SHA-256 of every regular file in the skill's
// directory (SKILL.md, scripts, resources), in path order, each as its
// relative path, its size and its content. It identifies exactly what was
// reviewed, so a config can pin a skill (skills.policy.trusted_hashes).
// Symbolic links are skipped. The format is "sha256:<hex>".
func (s *Skill) ContentHash() (string, error) {
	if s.fsys == nil {
		return "", errors.New("skill has no files to hash")
	}
	h := sha256.New()
	var total int64
	err := fs.WalkDir(s.fsys, s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // directories, and symbolic links, which could point anywhere
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > maxHashedBytes {
			return fmt.Errorf("skill directory is larger than %d MB", maxHashedBytes>>20)
		}
		f, err := s.fsys.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		rel := strings.TrimPrefix(strings.TrimPrefix(p, s.root), "/")
		fmt.Fprintf(h, "%s\x00%d\x00", rel, info.Size())
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// HostDir is the skill's directory on disk, or "" for built-in skills.
func (s *Skill) HostDir() string { return s.hostDir }

// ScriptPath returns the host path of a script shipped in the skill's
// directory (relative_path), checked through os.Root so neither ".." nor a
// symbolic link can lead outside the directory. Built-in skills have no
// host directory; use CopyTo.
func (s *Skill) ScriptPath(rel string) (string, error) {
	if s.hostDir == "" {
		return "", errors.New("built-in skill: its files aren't on disk")
	}
	if !inBundle(rel) {
		return "", fmt.Errorf("%q is outside the skill's directory", rel)
	}
	root, err := os.OpenRoot(s.hostDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q isn't a regular file", rel)
	}
	return filepath.Join(s.hostDir, filepath.FromSlash(rel)), nil
}

// CopyTo writes the skill's files into dir (for built-in skills, whose
// files are embedded). Symbolic links are skipped.
func (s *Skill) CopyTo(dir string) error {
	if s.fsys == nil {
		return errors.New("skill has no files")
	}
	return fs.WalkDir(s.fsys, s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, s.root), "/")
		target := filepath.Join(dir, filepath.FromSlash(rel))
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case !d.Type().IsRegular():
			return nil
		}
		b, err := fs.ReadFile(s.fsys, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}
