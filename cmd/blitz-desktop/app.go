package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	"github.com/retail-cortex/blitz/internal/config"
	"github.com/retail-cortex/blitz/internal/server"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is what the page can call in Go (bound by Wails): what the service
// can't do itself, namely report whether it runs, install it, and native
// dialogs.
type App struct {
	ctx    context.Context
	socket string
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// ServiceStatus says whether the service answers and whether it starts at
// login.
type ServiceStatus struct {
	Running   bool   `json:"running"`
	Installed bool   `json:"installed"`
	Socket    string `json:"socket"`
	// CLI is the blitz binary that installs the service ("" if none).
	CLI string `json:"cli"`
}

func (a *App) ServiceStatus() ServiceStatus {
	cli, _ := findCLI()
	return ServiceStatus{Running: server.Running(a.socket), Installed: loginItemInstalled(), Socket: a.socket, CLI: cli}
}

// InstallService runs `blitz service install`, which starts the
// service now and at every login.
func (a *App) InstallService() (string, error) {
	cli, err := findCLI()
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(a.ctx, cli, "service", "install").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return string(out), nil
}

// ChooseWorkspace asks for a directory to open ("" if cancelled).
func (a *App) ChooseWorkspace() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Open a workspace", CanCreateDirectories: true})
}

// findCLI finds the blitz command: next to this app's binary (as
// bundled), else on PATH.
func findCLI() (string, error) {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "blitz")
		if goruntime.GOOS == "windows" {
			p += ".exe"
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("blitz"); err == nil {
		return p, nil
	}
	return "", errors.New("the blitz command isn't installed")
}

// loginItemInstalled reports whether `blitz service install` has run.
func loginItemInstalled() bool {
	var p string
	switch goruntime.GOOS {
	case "darwin":
		p = "~/Library/LaunchAgents/dev.blitz.service.plist"
	case "linux":
		p = "~/.config/systemd/user/blitz.service"
	default:
		return false
	}
	_, err := os.Stat(config.ExpandHome(p))
	return err == nil
}
