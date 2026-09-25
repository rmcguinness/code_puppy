package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/images"
	"github.com/retail-cortex/code_puppy/pkg/tools"
)

// takeAttachments collects the images for this prompt: those queued with
// /attach, /paste or --image, plus @image mentions in the text. If a
// mentioned image can't be loaded the turn is not sent (so the user can fix
// it) and the queue is kept. On success the queue is emptied.
func takeAttachments(app *App, line string) ([]*images.Image, bool) {
	mentions := images.Mentions(line)
	if len(mentions) == 0 && len(app.Attachments) == 0 {
		return nil, true
	}
	out := append([]*images.Image{}, app.Attachments...)
	seen := map[string]bool{}
	for _, img := range out {
		seen[img.SHA256] = true
	}
	for _, p := range mentions {
		img, err := loadImage(app, p)
		if err != nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("attach.failed", "path", safe(p), "error", safe(err.Error())), Reset)
			fmt.Printf("%s%s%s\n", Dim, i18n.T("attach.not_sent"), Reset)
			return nil, false
		}
		if !seen[img.SHA256] {
			seen[img.SHA256] = true
			out = append(out, img)
		}
	}
	for _, img := range out {
		fmt.Printf("%s📎 %s%s\n", Dim, safe(img.Summary()), Reset)
	}
	app.Attachments = nil
	return out, true
}

func loadImage(app *App, path string) (*images.Image, error) {
	if app.Tools == nil {
		return nil, tools.ErrImagesDisabled
	}
	return app.Tools.LoadImage(path)
}

// AttachmentNote records attachments in the saved transcript.
func AttachmentNote(imgs []*images.Image) string {
	if len(imgs) == 0 {
		return ""
	}
	names := make([]string, len(imgs))
	for i, img := range imgs {
		names[i] = img.Name
	}
	return "\n[images: " + strings.Join(names, ", ") + "]"
}

func cmdAttach(args []string, app *App) {
	arg := strings.TrimSpace(strings.Join(args, " "))
	switch arg {
	case "":
		if len(app.Attachments) == 0 {
			fmt.Println(i18n.T("attach.usage"))
			return
		}
		fmt.Println(i18n.N("attach.pending", len(app.Attachments)))
		for _, img := range app.Attachments {
			fmt.Printf("  📎 %s\n", safe(img.Summary()))
		}
		return
	case "clear":
		n := len(app.Attachments)
		app.Attachments = nil
		fmt.Println(i18n.N("attach.cleared", n))
		return
	}
	// Accept "@path" (as Tab completes it) and quoted paths.
	path := strings.Trim(strings.TrimPrefix(arg, "@"), `"'`)
	img, err := loadImage(app, path)
	if err != nil {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("attach.failed", "path", safe(path), "error", safe(err.Error())), Reset)
		return
	}
	queue(app, img)
}

func cmdPaste(ctx context.Context, app *App) {
	if app.Tools == nil || app.Tools.Images() == nil {
		fmt.Println(i18n.T("attach.disabled"))
		return
	}
	data, err := images.ReadClipboard(ctx)
	if err != nil {
		if errors.Is(err, images.ErrNoClipboardImage) {
			fmt.Printf("%s%s%s\n", Yellow, i18n.T("attach.no_clipboard"), Reset)
		} else {
			fmt.Printf("%s❌ %s%s\n", Red, safe(err.Error()), Reset)
		}
		return
	}
	img, err := app.Tools.AddImage("clipboard-"+time.Now().Format("150405")+".png", data)
	if err != nil {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("attach.failed", "path", "clipboard", "error", safe(err.Error())), Reset)
		return
	}
	queue(app, img)
}

func queue(app *App, img *images.Image) {
	for _, have := range app.Attachments {
		if have.SHA256 == img.SHA256 {
			fmt.Println(i18n.T("attach.duplicate", "name", safe(img.Name)))
			return
		}
	}
	app.Attachments = append(app.Attachments, img)
	fmt.Printf("%s📎 %s%s\n", Green, i18n.T("attach.queued", "summary", safe(img.Summary())), Reset)
}
