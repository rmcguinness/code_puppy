package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestServiceInstallAndUninstall(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		t.Skip("login items are only supported on macOS and Linux")
	}
	isolate(t)
	var ran []string
	old := runSystem
	runSystem = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { runSystem = old })

	var out bytes.Buffer
	if err := serviceInstall(&out); err != nil {
		t.Fatal(err)
	}
	path, _ := unitPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("login item not written: %v", err)
	}
	unit := string(data)
	if !strings.Contains(unit, "serve") {
		t.Errorf("unit doesn't run serve:\n%s", unit)
	}
	switch goruntime.GOOS {
	case "darwin":
		dec := xml.NewDecoder(strings.NewReader(unit))
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("plist isn't well-formed: %v", err)
			}
		}
		if len(ran) != 2 || !strings.HasPrefix(ran[1], "launchctl bootstrap gui/") {
			t.Errorf("ran %q", ran)
		}
	case "linux":
		if len(ran) != 2 || ran[1] != "systemctl --user enable --now blitz.service" {
			t.Errorf("ran %q", ran)
		}
	}
	out.Reset()
	serviceStatus(&out)
	if !strings.Contains(out.String(), "login item: installed") {
		t.Errorf("status:\n%s", out.String())
	}

	ran = nil
	if err := serviceUninstall(&out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("login item left behind")
	}
	if len(ran) == 0 {
		t.Error("the service wasn't stopped")
	}
}

// A key only exported in the shell won't reach a login item.
func TestKeysOnlyInEnvironment(t *testing.T) {
	isolate(t)
	if got := keysOnlyInEnvironment(); len(got) != 0 {
		t.Errorf("no keys: %v", got)
	}
	t.Setenv("OPENAI_API_KEY", "sk-test-only-in-shell")
	if got := keysOnlyInEnvironment(); len(got) != 1 || got[0] != "OPENAI_API_KEY" {
		t.Errorf("shell-only key: %v", got)
	}
	if os.Getenv("OPENAI_API_KEY") != "sk-test-only-in-shell" {
		t.Error("the environment wasn't restored")
	}
}
