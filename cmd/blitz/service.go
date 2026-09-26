package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/retail-cortex/blitz/internal/config"
	"github.com/retail-cortex/blitz/internal/server"
	"github.com/spf13/cobra"
)

// Starting the service at login: a launchd agent on macOS, a systemd user
// unit on Linux.

const (
	launchdLabel = "dev.blitz.service"
	systemdUnit  = "blitz.service"
)

// runSystem runs a system command (launchctl, systemctl); tests replace it.
var runSystem = func(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func newServiceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Start the Blitz service at login (install, uninstall, status)",
		Long: `Installs a login item that runs 'blitz serve' for this user: a launchd
agent on macOS, a systemd user unit on Linux. The service then runs workers
on schedule and the CLI and desktop app attach to it.

A login item doesn't see your shell's environment: keep API keys in
~/.blitz/.env.toml, not only in exported variables.`,
	}
	cmd.AddCommand(
		&cobra.Command{Use: "install", Short: "Start the service now and at every login", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return serviceInstall(cmd.OutOrStdout()) }},
		&cobra.Command{Use: "uninstall", Short: "Stop the service and remove the login item", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return serviceUninstall(cmd.OutOrStdout()) }},
		&cobra.Command{Use: "status", Short: "Say whether the login item is installed and the service answering", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return serviceStatus(cmd.OutOrStdout()) }},
	)
	return cmd
}

// unitPath is where the login item is written.
func unitPath() (string, error) {
	switch goruntime.GOOS {
	case "darwin":
		return config.ExpandHome("~/Library/LaunchAgents/" + launchdLabel + ".plist"), nil
	case "linux":
		return config.ExpandHome("~/.config/systemd/user/" + systemdUnit), nil
	}
	return "", fmt.Errorf("starting the service at login isn't supported on %s: run 'blitz serve' yourself", goruntime.GOOS)
}

// launchdPlist is the launchd agent that runs bin serve and restarts it if
// it exits with an error.
func launchdPlist(bin, logFile string) string {
	x := html.EscapeString
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + x(bin) + `</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>` + x(logFile) + `</string>
	<key>StandardErrorPath</key>
	<string>` + x(logFile) + `</string>
</dict>
</plist>
`
}

// systemdUnitFile is the user unit that runs bin serve.
func systemdUnitFile(bin string) string {
	return `[Unit]
Description=Blitz service (workspaces and scheduled workers)

[Service]
ExecStart=` + strconv.Quote(bin) + ` serve
Restart=on-failure

[Install]
WantedBy=default.target
`
}

func serviceInstall(out io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return withCode(exitUsage, err)
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	switch goruntime.GOOS {
	case "darwin":
		logFile := config.ExpandHome("~/.blitz/logs/service.log")
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(launchdPlist(bin, logFile)), 0o644); err != nil {
			return err
		}
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = runSystem("launchctl", "bootout", domain+"/"+launchdLabel) // a previous install
		if err := runSystem("launchctl", "bootstrap", domain, path); err != nil {
			return err
		}
	case "linux":
		if err := os.WriteFile(path, []byte(systemdUnitFile(bin)), 0o644); err != nil {
			return err
		}
		if err := runSystem("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if err := runSystem("systemctl", "--user", "enable", "--now", systemdUnit); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "✓ The Blitz service starts at login (%s).\n", path)
	fmt.Fprintf(out, "   It runs %s serve; after upgrading Blitz, run 'blitz service install' again.\n", bin)
	if keys := keysOnlyInEnvironment(); len(keys) > 0 {
		fmt.Fprintf(out, "!  %s %s only in your shell's environment, which the service won't see: put %s in %s.\n",
			strings.Join(keys, ", "), plural(len(keys), "is", "are"), plural(len(keys), "it", "them"), filepath.Join(config.ConfigDir(""), ".env.toml"))
	}
	return nil
}

func serviceUninstall(out io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return withCode(exitUsage, err)
	}
	switch goruntime.GOOS {
	case "darwin":
		_ = runSystem("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchdLabel)
	case "linux":
		_ = runSystem("systemctl", "--user", "disable", "--now", systemdUnit)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if goruntime.GOOS == "linux" {
		_ = runSystem("systemctl", "--user", "daemon-reload")
	}
	fmt.Fprintln(out, "The Blitz service no longer starts at login.")
	return nil
}

func serviceStatus(out io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return withCode(exitUsage, err)
	}
	installed := "not installed"
	if _, err := os.Stat(path); err == nil {
		installed = "installed (" + path + ")"
	}
	running := "not running"
	if socket := server.DefaultSocket(); server.Running(socket) {
		running = "answering on " + socket
	}
	fmt.Fprintf(out, "login item: %s\nservice:    %s\n", installed, running)
	return nil
}

// keysOnlyInEnvironment names the API key variables set in the environment
// whose keys the config file doesn't also hold: a login item won't see them.
func keysOnlyInEnvironment() []string {
	vars := []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"}
	set := map[string]string{}
	for _, v := range vars {
		if val := os.Getenv(v); val != "" {
			set[v] = val
		}
	}
	if len(set) == 0 {
		return nil
	}
	// Load the configuration as the service would: without them.
	for v := range set {
		os.Unsetenv(v)
	}
	cfg, err := config.Load("")
	for v, val := range set {
		os.Setenv(v, val)
	}
	if err != nil {
		return nil
	}
	var out []string
	for _, v := range vars {
		if _, ok := set[v]; !ok {
			continue
		}
		have := ""
		switch v {
		case "GEMINI_API_KEY", "GOOGLE_API_KEY":
			have = cfg.LLM.Gemini.APIKey
		case "OPENAI_API_KEY":
			have = cfg.LLM.OpenAI.APIKey
		case "ANTHROPIC_API_KEY":
			have = cfg.LLM.Anthropic.APIKey
		}
		if have == "" {
			out = append(out, v)
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
