package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/i18n"
)

func TestLocaleCommand(t *testing.T) {
	defer i18n.SetCurrent(nil)
	i18n.SetCurrent(nil)
	app := newTestApp(t, nil)
	ctx := context.Background()
	var applied []string
	app.SetLocale = func(ctx context.Context, l *i18n.Localizer) (string, error) {
		applied = append(applied, l.Tag().String())
		return "/cfg/.env.toml", nil
	}

	out := captureStdout(t, func() { HandleCommand(ctx, "/locale", app) })
	for _, want := range []string{"Interface language: English (US) (en-US)", "es (Español)", "fr-CA (Français (Canada))", app.Cfg.UI.LocalesDir} {
		if !strings.Contains(out, want) {
			t.Errorf("/locale output lacks %q:\n%s", want, out)
		}
	}

	// The user's example: "/locale ES-sp" means Spanish.
	out = captureStdout(t, func() { HandleCommand(ctx, "/locale ES-sp", app) })
	if strings.Join(applied, ",") != "es" || i18n.Current().Tag().String() != "es" {
		t.Fatalf("applied %v, current %v", applied, i18n.Current().Tag())
	}
	if !strings.Contains(out, "Idioma de la interfaz: Español (es).") || !strings.Contains(out, "Guardado en /cfg/.env.toml.") {
		t.Errorf("confirmation should already be in Spanish:\n%s", out)
	}
	out = captureStdout(t, func() { HandleCommand(ctx, "/help", app) })
	if !strings.Contains(out, "Comandos de Code Puppy") || !strings.Contains(out, "/locale [code]") {
		t.Errorf("/help not translated:\n%s", out)
	}

	// A language without a catalog: model replies change, menus don't.
	out = captureStdout(t, func() { HandleCommand(ctx, "/locale japanese", app) })
	if !strings.Contains(out, "No translation catalog for Japanese") || i18n.Current().Tag().String() != "ja" {
		t.Errorf("ja output:\n%s", out)
	}

	out = captureStdout(t, func() { HandleCommand(ctx, "/locale en-US", app) })
	if !strings.Contains(out, "Interface language set to English (US) (en-US).") {
		t.Errorf("switching back:\n%s", out)
	}

	// Unknown input changes nothing.
	before := len(applied)
	out = captureStdout(t, func() { HandleCommand(ctx, "/locale zz-top-9", app) })
	if len(applied) != before || !strings.Contains(out, "Unknown language") {
		t.Errorf("unknown locale was applied: %v\n%s", applied, out)
	}

	// A save failure is reported but the session keeps the new language.
	app.SetLocale = func(context.Context, *i18n.Localizer) (string, error) { return "", errors.New("disk full") }
	out = captureStdout(t, func() { HandleCommand(ctx, "/locale fr", app) })
	if i18n.Current().Tag().String() != "fr" || !strings.Contains(out, "l’enregistrement a échoué : disk full") {
		t.Errorf("save failure:\n%s", out)
	}

	app.SetLocale = nil
	out = captureStdout(t, func() { HandleCommand(ctx, "/locale es", app) })
	if !strings.Contains(out, "n’est pas disponible") {
		t.Errorf("without SetLocale:\n%s", out)
	}
}
