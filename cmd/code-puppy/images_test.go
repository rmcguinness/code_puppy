package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"google.golang.org/genai"
)

func writePNG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 12, 8)))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAttachments(t *testing.T) {
	e := testEnv(t)
	ws := e.tools.Workspace().Dir()
	writePNG(t, filepath.Join(ws, "a.png"))
	writePNG(t, filepath.Join(ws, "b.png")) // same bytes as a.png: deduplicated

	var warnings []string
	warn := func(s string) { warnings = append(warnings, s) }
	got, err := loadAttachments(e, []string{"a.png"}, "diff mentions @b.png and @gone.png", warn)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d images, %v", len(got), err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "gone.png") {
		t.Errorf("a bad mention should warn, not fail: %q", warnings)
	}
	if _, err := loadAttachments(e, []string{"missing.png"}, "", warn); err == nil || !strings.Contains(err.Error(), "--image missing.png") {
		t.Errorf("a bad --image must fail: %v", err)
	}
}

func TestOneShotWithImage(t *testing.T) {
	e := testEnv(t)
	writePNG(t, filepath.Join(e.tools.Workspace().Dir(), "ui.png"))
	llm := runtime.NewMockLLM("gemini-3.8-flash", genai.NewContentFromText("a button", genai.RoleModel))
	if err := e.engine.SetModel(context.Background(), llm); err != nil {
		t.Fatal(err)
	}
	imgs, err := loadAttachments(e, []string{"ui.png"}, "", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := e.storage.CreateSession("", "t", "code-puppy")
	var out bytes.Buffer
	if err := runOneShot(context.Background(), e, oneShotOptions{prompt: "what is this?", sessionID: sess.ID, format: formatText, stdout: &out, images: imgs}); err != nil {
		t.Fatal(err)
	}
	last := llm.Requests[0].Contents[len(llm.Requests[0].Contents)-1]
	if len(last.Parts) != 2 || last.Parts[0].InlineData == nil || last.Parts[1].Text != "what is this?" {
		t.Errorf("request parts: %+v", last.Parts)
	}
	if !strings.Contains(out.String(), "a button") {
		t.Errorf("output: %s", out.String())
	}
}

func TestImageFlagRejectsMissingFile(t *testing.T) {
	isolate(t)
	t.Setenv("GEMINI_API_KEY", "AIzaSyTESTKEY-1234567890abcdefghijklmnop")
	_, err := runCLI(t, "--image", "nope.png", "describe it")
	if exitCodeFor(err) != exitUsage || !strings.Contains(err.Error(), "nope.png") {
		t.Errorf("exit %d: %v", exitCodeFor(err), err)
	}
}
