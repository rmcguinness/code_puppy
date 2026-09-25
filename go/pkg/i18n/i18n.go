// Package i18n translates user-interface text.
//
// Each language is one JSON catalog:
//
//	{
//	  "meta": {"locale": "es", "name": "Español", "english_name": "Spanish"},
//	  "messages": {
//	    "session.resumed": "Sesión {id} reanudada",
//	    "files.count.one": "{count} archivo",
//	    "files.count.other": "{count} archivos"
//	  }
//	}
//
// Catalogs are embedded for the shipped languages and also loaded from extra
// directories (e.g. ~/.code_puppy/locales), so a language can be added, or a
// shipped translation corrected, without rebuilding. Lookups fall back along
// the locale chain (fr-CA → fr → en-US), so a partial catalog still works.
//
// Placeholders are {name}; values are passed as name/value pairs:
//
//	i18n.T("session.resumed", "id", id)
//
// Plurals use ".one"/".other" suffixed keys selected by N.
package i18n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// DefaultLocale is the source language; every key must exist in it.
const DefaultLocale = "en-US"

// PseudoLocale renders English text with accents and brackets, to spot text
// that was never moved into a catalog.
const PseudoLocale = "en-XA"

//go:embed locales/*.json
var embedded embed.FS

// Meta describes a catalog.
type Meta struct {
	Locale      string `json:"locale"`
	Name        string `json:"name"`         // in the language itself
	EnglishName string `json:"english_name"` // for users who can't read it
}

// Catalog is one language's messages.
type Catalog struct {
	Meta     Meta              `json:"meta"`
	Messages map[string]string `json:"messages"`
	tag      language.Tag
	sources  []string
}

// Bundle holds every loaded catalog.
type Bundle struct {
	mu       sync.RWMutex
	catalogs map[language.Tag]*Catalog
}

var placeholderRE = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// NewBundle loads the embedded catalogs, then catalogs in dirs (later files
// add keys and override earlier translations). Problems with external files
// are returned but don't stop loading.
func NewBundle(dirs ...string) (*Bundle, []error) {
	b := &Bundle{catalogs: map[language.Tag]*Catalog{}}
	var errs []error
	entries, _ := fs.ReadDir(embedded, "locales")
	for _, e := range entries {
		data, _ := fs.ReadFile(embedded, "locales/"+e.Name())
		if err := b.add(data, "builtin:"+e.Name()); err != nil {
			panic(fmt.Sprintf("embedded catalog %s: %v", e.Name(), err)) // caught by tests
		}
	}
	for _, dir := range dirs {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, f.Name())
			data, err := os.ReadFile(path)
			if err == nil {
				err = b.add(data, path)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("locale catalog %s: %w", path, err))
			}
		}
	}
	if b.catalogs[language.MustParse(DefaultLocale)] == nil {
		panic("i18n: default catalog missing")
	}
	return b, errs
}

func (b *Bundle) add(data []byte, source string) error {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	tag, err := language.Parse(c.Meta.Locale)
	if err != nil {
		return fmt.Errorf("invalid meta.locale %q: %w", c.Meta.Locale, err)
	}
	if len(c.Messages) == 0 {
		return errors.New("catalog has no messages")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if prev := b.catalogs[tag]; prev != nil {
		for k, v := range c.Messages {
			prev.Messages[k] = v
		}
		if c.Meta.Name != "" {
			prev.Meta.Name = c.Meta.Name
		}
		if c.Meta.EnglishName != "" {
			prev.Meta.EnglishName = c.Meta.EnglishName
		}
		prev.sources = append(prev.sources, source)
		return nil
	}
	c.tag, c.sources = tag, []string{source}
	if c.Meta.EnglishName == "" {
		c.Meta.EnglishName = display.English.Tags().Name(tag)
	}
	if c.Meta.Name == "" {
		c.Meta.Name = display.Self.Name(tag)
	}
	b.catalogs[tag] = &c
	return nil
}

// Available lists catalogs sorted by locale.
func (b *Bundle) Available() []Meta {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Meta, 0, len(b.catalogs))
	for _, c := range b.catalogs {
		out = append(out, c.Meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Locale < out[j].Locale })
	return out
}

// Catalog returns the catalog for exactly tag, if loaded.
func (b *Bundle) Catalog(tag string) (*Catalog, bool) {
	t, err := language.Parse(tag)
	if err != nil {
		return nil, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.catalogs[t]
	return c, ok
}

// ErrUnknownLocale is returned when input can't be understood as a locale.
var ErrUnknownLocale = errors.New("unknown locale")

// Resolve turns user input into a locale tag. It accepts BCP 47 tags in any
// case with "-" or "_" ("es", "es-ES", "ES_es"), tolerates an invalid region
// by keeping the language ("ES-sp" → "es"), and matches catalog names in
// English or the language itself ("spanish", "español").
func (b *Bundle) Resolve(input string) (language.Tag, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return language.Und, ErrUnknownLocale
	}
	b.mu.RLock()
	for t, c := range b.catalogs {
		if strings.EqualFold(s, c.Meta.Name) || strings.EqualFold(s, c.Meta.EnglishName) {
			b.mu.RUnlock()
			return t, nil
		}
	}
	b.mu.RUnlock()
	for _, t := range display.Supported.Tags() {
		if strings.EqualFold(s, display.English.Tags().Name(t)) || strings.EqualFold(s, display.Self.Name(t)) {
			return t, nil
		}
	}

	s = strings.ReplaceAll(s, "_", "-")
	if strings.EqualFold(s, PseudoLocale) {
		return language.MustParse(PseudoLocale), nil
	}
	if t, err := language.Parse(s); err == nil && !t.IsRoot() {
		return t, nil
	}
	base, _, _ := strings.Cut(s, "-")
	if t, err := language.Parse(base); err == nil && !t.IsRoot() {
		return t, nil
	}
	return language.Und, fmt.Errorf("%w: %q", ErrUnknownLocale, input)
}

// Localizer renders messages for one locale.
type Localizer struct {
	tag    language.Tag
	chain  []*Catalog // most specific first, ending with the default
	pseudo bool
}

// Localizer builds a localizer for tag, falling back through its parents
// and finally the default locale.
func (b *Bundle) Localizer(tag language.Tag) *Localizer {
	b.mu.RLock()
	defer b.mu.RUnlock()
	l := &Localizer{tag: tag, pseudo: tag == language.MustParse(PseudoLocale)}
	seen := map[language.Tag]bool{}
	for t := tag; ; t = t.Parent() {
		if c := b.catalogs[t]; c != nil && !seen[t] {
			l.chain = append(l.chain, c)
			seen[t] = true
		}
		// "es-ES" has no exact catalog but "es" may; Parent handles that.
		// Also try the bare base language for tags whose parent skips it.
		if base, conf := t.Base(); conf != language.No {
			if bt, err := language.Compose(base); err == nil {
				if c := b.catalogs[bt]; c != nil && !seen[bt] {
					l.chain = append(l.chain, c)
					seen[bt] = true
				}
			}
		}
		if t.IsRoot() {
			break
		}
	}
	// No catalog for the language itself: borrow a regional one ("fr" →
	// "fr-CA"), choosing the lowest tag so the result is stable.
	if len(l.chain) == 0 {
		want, _ := tag.Base()
		var best *Catalog
		for t, c := range b.catalogs {
			if base, _ := t.Base(); base == want && (best == nil || t.String() < best.tag.String()) {
				best = c
			}
		}
		if best != nil {
			l.chain = append(l.chain, best)
			seen[best.tag] = true
		}
	}
	def := language.MustParse(DefaultLocale)
	if !seen[def] {
		l.chain = append(l.chain, b.catalogs[def])
	}
	return l
}

// Tag returns the requested locale.
func (l *Localizer) Tag() language.Tag { return l.tag }

// HasCatalog reports whether any non-default catalog serves this locale.
func (l *Localizer) HasCatalog() bool {
	return l.pseudo || l.IsEnglish() || len(l.chain) > 1 || l.chain[0].tag == l.tag
}

// LanguageName is the locale's English display name ("Spanish").
func (l *Localizer) LanguageName() string {
	return display.English.Tags().Name(l.tag)
}

// NativeName is the locale's name in its own language ("español").
func (l *Localizer) NativeName() string {
	for _, c := range l.chain {
		if c.tag == l.tag && c.Meta.Name != "" {
			return c.Meta.Name
		}
	}
	if n := display.Self.Name(l.tag); n != "" {
		return n
	}
	return l.LanguageName()
}

// IsEnglish reports whether the locale's language is English.
func (l *Localizer) IsEnglish() bool {
	base, _ := l.tag.Base()
	return base.String() == "en"
}

// Lookup returns the raw message for key and whether it was found.
func (l *Localizer) Lookup(key string) (string, bool) {
	for _, c := range l.chain {
		if m, ok := c.Messages[key]; ok {
			return m, true
		}
	}
	return "", false
}

// T renders key with name/value argument pairs. Missing keys render as the
// key itself so the problem is visible rather than silent.
func (l *Localizer) T(key string, args ...any) string {
	msg, ok := l.Lookup(key)
	if !ok {
		return key
	}
	if l.pseudo {
		msg = pseudoize(msg)
	}
	return fill(msg, args)
}

// N renders the plural form of key for count; count is also available as
// {count}.
func (l *Localizer) N(key string, count int, args ...any) string {
	form := key + "." + pluralCategory(l.tag, count)
	if _, ok := l.Lookup(form); !ok {
		form = key + ".other"
	}
	return l.T(form, append([]any{"count", count}, args...)...)
}

func fill(msg string, args []any) string {
	if len(args) == 0 || !strings.Contains(msg, "{") {
		return msg
	}
	vals := make(map[string]string, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		vals[fmt.Sprint(args[i])] = fmt.Sprint(args[i+1])
	}
	return placeholderRE.ReplaceAllStringFunc(msg, func(m string) string {
		if v, ok := vals[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

// pluralCategory returns "one" or "other" using CLDR's rules for the common
// cases; languages without a singular form always get "other".
func pluralCategory(tag language.Tag, n int) string {
	base, _ := tag.Base()
	switch base.String() {
	case "ja", "zh", "ko", "th", "vi", "id":
		return "other"
	case "fr":
		if n == 0 || n == 1 {
			return "one"
		}
	case "pt":
		if region, _ := tag.Region(); region.String() == "BR" && (n == 0 || n == 1) {
			return "one"
		}
		if n == 1 {
			return "one"
		}
	default:
		if n == 1 {
			return "one"
		}
	}
	return "other"
}

var pseudoMap = strings.NewReplacer("a", "á", "e", "é", "i", "í", "o", "ö", "u", "ü", "A", "Å", "E", "É", "O", "Ø", "c", "ç", "n", "ñ")

func pseudoize(msg string) string {
	// Accent letters outside {placeholders}.
	var sb strings.Builder
	last := 0
	for _, loc := range placeholderRE.FindAllStringIndex(msg, -1) {
		sb.WriteString(pseudoMap.Replace(msg[last:loc[0]]))
		sb.WriteString(msg[loc[0]:loc[1]])
		last = loc[1]
	}
	sb.WriteString(pseudoMap.Replace(msg[last:]))
	return "⟦" + sb.String() + "⟧"
}

// Placeholders returns the {names} used in msg.
func Placeholders(msg string) []string {
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(msg, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// Problems reports translation issues in catalog tag relative to the default:
// keys the default doesn't have, and placeholders that don't match.
func (b *Bundle) Problems(tag string) []string {
	c, ok := b.Catalog(tag)
	if !ok {
		return []string{"no catalog for " + tag}
	}
	def, _ := b.Catalog(DefaultLocale)
	var out []string
	for k, v := range c.Messages {
		base, found := def.Messages[k]
		if !found {
			// A language may use plural forms English doesn't: compare
			// against the English ".other" form of the same key.
			for _, suffix := range []string{".one", ".other"} {
				if stem, ok := strings.CutSuffix(k, suffix); ok {
					if other, ok := def.Messages[stem+".other"]; ok {
						base, found = other, true
					}
				}
			}
			if !found {
				out = append(out, "unknown key "+k)
				continue
			}
		}
		if strings.Join(Placeholders(v), ",") != strings.Join(Placeholders(base), ",") {
			out = append(out, fmt.Sprintf("%s: placeholders %v, English has %v", k, Placeholders(v), Placeholders(base)))
		}
	}
	sort.Strings(out)
	return out
}

// Missing lists default-locale keys the catalog doesn't translate.
func (b *Bundle) Missing(tag string) []string {
	c, ok := b.Catalog(tag)
	if !ok {
		return nil
	}
	def, _ := b.Catalog(DefaultLocale)
	var out []string
	for k := range def.Messages {
		if _, ok := c.Messages[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Package-level localizer used by the UI.

var (
	defaultBundle     *Bundle
	defaultBundleOnce sync.Once
	current           atomic.Pointer[Localizer]
)

// Default returns the bundle of embedded catalogs.
func Default() *Bundle {
	defaultBundleOnce.Do(func() { defaultBundle, _ = NewBundle() })
	return defaultBundle
}

// SetCurrent sets the localizer used by T and N.
func SetCurrent(l *Localizer) { current.Store(l) }

// Current returns the active localizer (English until SetCurrent).
func Current() *Localizer {
	if l := current.Load(); l != nil {
		return l
	}
	l := Default().Localizer(language.MustParse(DefaultLocale))
	current.CompareAndSwap(nil, l)
	return current.Load()
}

// T renders key with the active localizer.
func T(key string, args ...any) string { return Current().T(key, args...) }

// N renders a plural with the active localizer.
func N(key string, count int, args ...any) string { return Current().N(key, count, args...) }

// ReplyInstruction tells the model to answer in the user's language; empty
// for English.
func ReplyInstruction(l *Localizer) string {
	if l == nil || l.IsEnglish() || l.pseudo {
		return ""
	}
	name := l.LanguageName()
	return fmt.Sprintf("\n\n## Response Language\nThe user's interface language is %s (%s). Write your replies to the user in %s unless they ask for another language. Keep code, identifiers, file paths, commands, and tool output exactly as they are.",
		name, l.tag, name)
}
