package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedSkills(t *testing.T) {
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("failed to create skill provider: %v", err)
	}

	skills := p.List()
	if len(skills) < 3 {
		t.Errorf("expected at least 3 embedded skills, got %d", len(skills))
	}

	tdd, ok := p.Get("testing-tdd")
	if !ok || tdd == nil {
		t.Fatalf("expected to find 'testing-tdd' skill")
	}

	if len(tdd.Tags) == 0 {
		t.Errorf("expected tags for testing-tdd skill")
	}

	matches := p.Search("git")
	if len(matches) == 0 {
		t.Errorf("expected search for 'git' to return at least 1 match")
	}
}

func TestExternalSkillDiscovery(t *testing.T) {
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("failed to create skill provider: %v", err)
	}

	tmpDir := t.TempDir()
	customSkillDir := filepath.Join(tmpDir, "my-skill")
	_ = os.MkdirAll(customSkillDir, 0755)

	content := `---
name: custom-docker
description: Custom Docker deployment recipes
tags: [docker, deploy]
version: "2.0.0"
---
# Instructions
Deploy docker containers reliably.
`
	_ = os.WriteFile(filepath.Join(customSkillDir, "SKILL.md"), []byte(content), 0644)
	_ = os.WriteFile(filepath.Join(customSkillDir, "docker-compose.yml"), []byte("version: '3'"), 0644)

	err = p.DiscoverExternal([]string{tmpDir})
	if err != nil {
		t.Fatalf("DiscoverExternal failed: %v", err)
	}

	skill, ok := p.Get("custom-docker")
	if !ok || skill == nil {
		t.Fatalf("expected to find discovered custom-docker skill")
	}

	if len(skill.Resources) != 1 || skill.Resources[0] != "docker-compose.yml" {
		t.Errorf("expected resource docker-compose.yml, got %v", skill.Resources)
	}
}
