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

	"github.com/retail-cortex/code_puppy/internal/runtime"
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

func TestOneShotWithImage(t *testing.T) {
	e := testEnv(t)
	writePNG(t, filepath.Join(e.Tools().Workspace().Dir(), "ui.png"))
	llm := runtime.NewMockLLM("gemini-3.8-flash", genai.NewContentFromText("a button", genai.RoleModel))
	if err := e.Engine().SetModel(context.Background(), llm); err != nil {
		t.Fatal(err)
	}
	imgs, err := e.LoadAttachments([]string{"ui.png"}, "", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := e.Storage().CreateSession("", "t", "code-puppy")
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
