package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoClipboardImage means the clipboard holds no image (or no clipboard
// tool is installed).
var ErrNoClipboardImage = errors.New("no image on the clipboard")

// ReadClipboard returns the clipboard image as PNG bytes using the
// platform's own tools: osascript on macOS, wl-paste or xclip on Linux, and
// PowerShell on Windows. It is a variable so tests can replace it.
var ReadClipboard = readClipboard

func readClipboard(ctx context.Context) ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return viaTempFile(ctx, func(p string) *exec.Cmd {
			script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
try
	write (the clipboard as «class PNGf») to f
	close access f
on error
	close access f
	error "no image"
end try`, p)
			return exec.CommandContext(ctx, "osascript", "-e", script)
		})
	case "windows":
		return viaTempFile(ctx, func(p string) *exec.Cmd {
			script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; $i = [Windows.Forms.Clipboard]::GetImage(); if ($i -eq $null) { exit 1 }; $i.Save('%s', [Drawing.Imaging.ImageFormat]::Png)`,
				strings.ReplaceAll(p, "'", "''"))
			return exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", script)
		})
	default:
		candidates := [][]string{
			{"wl-paste", "--no-newline", "--type", "image/png"},
			{"xclip", "-selection", "clipboard", "-target", "image/png", "-out"},
		}
		for _, c := range candidates {
			if _, err := exec.LookPath(c[0]); err != nil {
				continue
			}
			var out bytes.Buffer
			cmd := exec.CommandContext(ctx, c[0], c[1:]...)
			cmd.Stdout = &out
			if cmd.Run() == nil && out.Len() > 0 {
				return out.Bytes(), nil
			}
		}
		return nil, fmt.Errorf("%w (Linux needs wl-paste or xclip)", ErrNoClipboardImage)
	}
}

func viaTempFile(ctx context.Context, build func(path string) *exec.Cmd) ([]byte, error) {
	dir, err := os.MkdirTemp("", "code-puppy-clip-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "clipboard.png")
	if err := build(p).Run(); err != nil {
		return nil, ErrNoClipboardImage
	}
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return nil, ErrNoClipboardImage
	}
	return data, nil
}
