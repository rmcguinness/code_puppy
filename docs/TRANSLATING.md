# Translating Code Puppy

Each language is one JSON file. The shipped catalogs live in [`pkg/i18n/locales/`](../pkg/i18n/locales): `en-US.json` is the source, and every key must exist there.

## Add or fix a language without rebuilding

1. Copy [`en-US.json`](../pkg/i18n/locales/en-US.json) to `~/.code_puppy/locales/<code>.json`, for example `de.json`. The directory is set by `[ui] locales_dir`.
2. Set `meta.locale` to a BCP 47 code (`de`, `pt-BR`). You can leave `name` and `english_name` empty; they default to the CLDR names ("Deutsch", "German").
3. Translate the values, then run `/locale de`.

You don't have to translate everything: missing keys fall back to the parent language and then to English (`fr-CA` → `fr` → `en-US`). To correct a single phrase in a shipped language, create a file with the same `meta.locale` that holds only that key; it overrides the built-in text and keeps the rest. Files that don't parse are reported as warnings at startup, and the remaining files still load.

## Rules

- **Placeholders** like `{name}` or `{count}` must appear in the translation, spelled exactly as in English. Their order can change.
- **Plurals** use `.one` and `.other` keys (`session.messages.one`, `session.messages.other`). French treats 0 as singular. Japanese, Chinese, Korean, Thai, Vietnamese and Indonesian always use `.other`.
- **Answer letters** in brackets (`[y]`, `[s]`, `[a]`, `[n]`, `[d]`, `[k]`, `[w]`, `[c]`) are what the user types. Keep them even when the word changes: `[y] sí`, not `[s] sí`.
- **Keep literal** anything the user types or the program reads: commands (`/diff git`), config keys (`[context] compaction = false`), file names.

## Checking a catalog

To ship a catalog, add it to `pkg/i18n/locales/`; the build embeds it. `go test ./pkg/i18n` then checks every shipped catalog: no missing keys, no unknown keys, matching placeholders, and answer letters intact. For a file in `~/.code_puppy/locales`, run `/locale <code>` and look through `/help`, `/cost` and an approval prompt.

To find text that was never moved into a catalog, use the pseudo-locale: `/locale en-XA`. It shows every catalog string accented and in brackets (`⟦Éxít çáñçélléd.⟧`), so any plain English left on screen was never moved into a catalog. `TestNoUntranslatedOutput` catches most of these in CI.

## What is translated

The interactive interface is translated: the REPL, slash commands, approval and exit prompts, the banner and the usage line. With a non-English locale, the model is also told to reply in that language.

These stay in English:
- `doctor` output, `--help` and command-line errors, so they can be pasted into bug reports.
- Text the model reads, such as tool descriptions and tool results.
- Error details that come from lower layers.
