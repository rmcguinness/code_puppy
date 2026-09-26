package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/genai"
)

func TestViableLinks(t *testing.T) {
	in := []tools.SearchResult{
		{URL: "https://a.example/doc#intro"},
		{URL: "https://A.example/doc"}, // same page
		{URL: "https://b.example/paper.PDF"},
		{URL: "ftp://c.example/"},
		{URL: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/x"},
		{URL: "https://d.example/"},
		{URL: "https://e.example/"},
		{URL: "https://f.example/"},
		{URL: "https://g.example/"},
		{URL: "https://h.example/"},
	}
	got := viableLinks(in, 5)
	var urls []string
	for _, r := range got {
		urls = append(urls, r.URL)
	}
	want := "https://a.example/doc#intro https://d.example/ https://e.example/ https://f.example/ https://g.example/"
	if strings.Join(urls, " ") != want {
		t.Fatalf("got %v", urls)
	}
}

func TestUsageContextAndSessionSearch(t *testing.T) {
	w, llm := openTestWith(t, nil, text("pineapple noted"))
	llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 10}
	if _, err := w.SessionUsage(); !errors.Is(err, ErrNoActiveSession) {
		t.Errorf("usage without a session: %v", err)
	}
	if _, err := w.Context(); !errors.Is(err, ErrNoActiveSession) {
		t.Errorf("context without a session: %v", err)
	}
	sid := newSession(t, w).ID
	if _, err := w.Run(context.Background(), sid, Turn{Text: "remember pineapple"}, ignore); err != nil {
		t.Fatal(err)
	}
	if u, err := w.SessionUsage(); err != nil || u.Calls != 1 || u.Input != 100 {
		t.Errorf("usage %+v %v", u, err)
	}
	if c, err := w.Context(); err != nil || c.Tokens != 100 {
		t.Errorf("context %+v %v", c, err)
	}
	if found, prompt := w.SearchSession("pineapple"); found == 0 || !strings.Contains(prompt, "pineapple") {
		t.Errorf("session search: %d %q", found, prompt)
	}
	if found, _ := w.SearchSession("mango"); found != 0 {
		t.Errorf("found %d passages for mango", found)
	}
}

func TestMemoryAndLocale(t *testing.T) {
	defer i18n.SetCurrent(nil)
	w := openTest(t)
	ctx := context.Background()
	if paths, err := w.ReloadMemory(ctx); err != nil || len(paths) != 0 {
		t.Fatalf("memory before: %v %v", paths, err)
	}
	p, err := w.AddMemory(ctx, "always run go vet")
	if err != nil || filepath.Base(p) != "PUPPY.md" {
		t.Fatalf("add: %q %v", p, err)
	}
	if paths, _ := w.ReloadMemory(ctx); len(paths) != 1 {
		t.Errorf("memory after: %v", paths)
	}

	if _, err := w.SetLocale(ctx, "zz-top-9"); !errors.Is(err, ErrUnknownLocale) {
		t.Errorf("unknown locale: %v", err)
	}
	res, err := w.SetLocale(ctx, "japanese")
	if err != nil || res.Tag != "ja" || res.HasCatalog || res.Saved.Err != nil || i18n.Current().Tag().String() != "ja" {
		t.Fatalf("ja: %+v %v", res, err)
	}
	if got := savedConfig(t).UI.Locale; got != "ja" {
		t.Errorf("saved locale %q", got)
	}
	if list, _ := w.AvailableLocales(); len(list) < 3 {
		t.Errorf("locales %v", list)
	}
}
