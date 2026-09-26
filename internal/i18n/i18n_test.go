package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/language"
)

func mustBundle(t *testing.T, dirs ...string) *Bundle {
	t.Helper()
	b, errs := NewBundle(dirs...)
	if len(errs) > 0 {
		t.Fatalf("NewBundle: %v", errs)
	}
	return b
}

func TestEmbeddedCatalogs(t *testing.T) {
	b := mustBundle(t)
	var got []string
	for _, m := range b.Available() {
		got = append(got, m.Locale)
		if m.Name == "" || m.EnglishName == "" {
			t.Errorf("%s: missing names: %+v", m.Locale, m)
		}
	}
	if strings.Join(got, ",") != "en-US,es,fr-CA" {
		t.Fatalf("available = %v", got)
	}
}

// Shipped translations must be complete and keep every placeholder, or a
// value (a file name, an error) silently disappears from the message.
func TestShippedTranslationsComplete(t *testing.T) {
	b := mustBundle(t)
	for _, tag := range []string{"es", "fr-CA"} {
		if m := b.Missing(tag); len(m) > 0 {
			t.Errorf("%s is missing %d keys: %v", tag, len(m), m)
		}
		if p := b.Problems(tag); len(p) > 0 {
			t.Errorf("%s problems:\n  %s", tag, strings.Join(p, "\n  "))
		}
	}
}

// Approval and exit answers are single letters the code matches; every
// translation must keep offering the same letters.
func TestTranslationsKeepAnswerLetters(t *testing.T) {
	b := mustBundle(t)
	keys := map[string][]string{
		"approve.options":     {"[y]", "[s]", "[a]"},
		"approve.yes":         {"[y]"},
		"approve.no":          {"[n]"},
		"approve.show_diff":   {"[d]"},
		"exit.choices":        {"[k", "[w"},
		"exit.choices_cancel": {"[c"},
		"approvals.none":      {"[s]", "[a]"},
	}
	for _, m := range b.Available() {
		c, _ := b.Catalog(m.Locale)
		for key, letters := range keys {
			for _, l := range letters {
				if !strings.Contains(c.Messages[key], l) {
					t.Errorf("%s %s = %q lacks %s", m.Locale, key, c.Messages[key], l)
				}
			}
		}
	}
}

func TestResolve(t *testing.T) {
	b := mustBundle(t)
	cases := map[string]string{
		"es": "es", "ES": "es", "es-ES": "es-ES", "es_ES": "es-ES", "ES-sp": "es",
		"spanish": "es", "Español": "es", "español": "es",
		"fr-CA": "fr-CA", "fr_ca": "fr-CA", "French (Canada)": "fr-CA",
		"en-US": "en-US", "de": "de", "German": "de", "japanese": "ja", "日本語": "ja", "ja-JP": "ja-JP", "en-XA": "en-XA",
	}
	for in, want := range cases {
		got, err := b.Resolve(in)
		if err != nil || got.String() != want {
			t.Errorf("Resolve(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "klingonese", "123", "x"} {
		if _, err := b.Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) should fail", bad)
		}
	}
}

func TestFallbackChain(t *testing.T) {
	b := mustBundle(t)
	loc := func(s string) *Localizer { return b.Localizer(language.MustParse(s)) }

	if got := loc("es-MX").T("recap.you"); got != "tú" {
		t.Errorf("es-MX should use the es catalog, got %q", got)
	}
	if got := loc("fr").T("exit.cancelled"); got != "Fermeture annulée." {
		t.Errorf("fr should borrow fr-CA, got %q", got)
	}
	de := loc("de")
	if de.HasCatalog() || de.T("exit.cancelled") != "Exit cancelled." {
		t.Errorf("de should fall back to English: %v %q", de.HasCatalog(), de.T("exit.cancelled"))
	}
	if !loc("en-GB").HasCatalog() || !loc("es").HasCatalog() {
		t.Error("English variants and es have catalogs")
	}
	if got := loc("es").T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing keys render as the key, got %q", got)
	}
}

func TestPlaceholdersAndPlurals(t *testing.T) {
	b := mustBundle(t)
	en := b.Localizer(language.MustParse("en-US"))
	if got := en.T("agent.current", "name", "Helios", "id", "helios"); got != "Current agent: Helios (helios)" {
		t.Errorf("got %q", got)
	}
	if got := en.T("agent.current", "name", "X"); got != "Current agent: X ({id})" {
		t.Errorf("an unfilled placeholder should stay visible, got %q", got)
	}
	for n, want := range map[int]string{0: "0 messages", 1: "1 message", 2: "2 messages"} {
		if got := en.N("session.messages", n); got != want {
			t.Errorf("en N(%d) = %q", n, got)
		}
	}
	// French treats 0 as singular.
	fr := b.Localizer(language.MustParse("fr-CA"))
	for n, want := range map[int]string{0: "0 règle révoquée.", 1: "1 règle révoquée.", 3: "3 règles révoquées."} {
		if got := fr.N("approvals.revoked", n); got != want {
			t.Errorf("fr N(%d) = %q, want %q", n, got, want)
		}
	}
	// Japanese has no singular: always "other".
	if got := pluralCategory(language.Japanese, 1); got != "other" {
		t.Errorf("ja plural = %s", got)
	}
}

func TestPseudoLocale(t *testing.T) {
	b := mustBundle(t)
	l := b.Localizer(language.MustParse(PseudoLocale))
	got := l.T("agent.current", "name", "helios", "id", "x")
	if !strings.HasPrefix(got, "⟦") || !strings.Contains(got, "helios") || strings.Contains(got, "Current") {
		t.Errorf("pseudo = %q", got)
	}
	if ReplyInstruction(l) != "" {
		t.Error("pseudo-locale should not change the model's language")
	}
}

func TestExternalCatalogs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("de.json", `{"meta":{"locale":"de"},"messages":{"exit.cancelled":"Beenden abgebrochen.","session.messages.one":"{count} Nachricht","session.messages.other":"{count} Nachrichten"}}`)
	write("es-fix.json", `{"meta":{"locale":"es"},"messages":{"recap.puppy":"perrito"}}`)
	write("broken.json", `{not json`)
	write("nolocale.json", `{"meta":{"locale":"???"},"messages":{"a":"b"}}`)
	write("notes.txt", "ignored")

	b, errs := NewBundle(dir, filepath.Join(dir, "missing"))
	if len(errs) != 2 {
		t.Fatalf("want 2 errors (broken, nolocale), got %v", errs)
	}
	de := b.Localizer(language.German)
	if !de.HasCatalog() || de.T("exit.cancelled") != "Beenden abgebrochen." || de.N("session.messages", 2) != "2 Nachrichten" {
		t.Errorf("de catalog not used")
	}
	if de.T("exit.force_quit") != "Force quit." {
		t.Error("untranslated keys should fall back to English")
	}
	if c, _ := b.Catalog("de"); c.Meta.EnglishName != "German" || c.Meta.Name != "Deutsch" {
		t.Errorf("names should default from CLDR: %+v", c.Meta)
	}
	es := b.Localizer(language.Spanish)
	if es.T("recap.puppy") != "perrito" || es.T("recap.you") != "tú" {
		t.Error("an external file should override single keys and keep the rest")
	}
	if tag, err := b.Resolve("german"); err != nil || tag != language.German {
		t.Errorf("Resolve(german) = %v, %v", tag, err)
	}
}

func TestProblemsDetectsBadTranslations(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "it.json"), []byte(`{"meta":{"locale":"it"},"messages":{
		"agent.current":"Agente: {nome}",
		"made.up":"x",
		"session.messages.one":"{count} messaggio"}}`), 0o600)
	b := mustBundle(t, dir)
	p := strings.Join(b.Problems("it"), "\n")
	if !strings.Contains(p, "agent.current: placeholders") || !strings.Contains(p, "unknown key made.up") {
		t.Errorf("problems = %s", p)
	}
	if strings.Contains(p, "session.messages.one") {
		t.Errorf("valid plural form flagged: %s", p)
	}
}

func TestReplyInstruction(t *testing.T) {
	b := mustBundle(t)
	if ReplyInstruction(b.Localizer(language.MustParse("en-US"))) != "" || ReplyInstruction(nil) != "" {
		t.Error("English needs no reply instruction")
	}
	got := ReplyInstruction(b.Localizer(language.MustParse("es")))
	if !strings.Contains(got, "Spanish") || !strings.Contains(got, "file paths") {
		t.Errorf("got %q", got)
	}
	// Languages without a catalog still get replies in that language.
	if got := ReplyInstruction(b.Localizer(language.MustParse("ja"))); !strings.Contains(got, "Japanese") {
		t.Errorf("got %q", got)
	}
}

func TestCurrentDefaultsToEnglish(t *testing.T) {
	defer SetCurrent(nil)
	SetCurrent(nil)
	if Current().Tag().String() != DefaultLocale || T("exit.cancelled") != "Exit cancelled." {
		t.Error("default should be en-US")
	}
	SetCurrent(Default().Localizer(language.Spanish))
	if T("exit.cancelled") != "Salida cancelada." || N("session.messages", 1) != "1 mensaje" {
		t.Error("SetCurrent not applied")
	}
}
