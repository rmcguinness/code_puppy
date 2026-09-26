package runtime

import (
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

// WebSearchPrompt hands the links the user's /search web picked to the
// agent to read. They are approved for the turn (tools.WithFetchGrants);
// answer is the search engine's own summary, if it gave one.
func WebSearchPrompt(terms string, links []tools.SearchResult, answer string) string {
	var b strings.Builder
	b.WriteString("I searched the web for: " + terms + "\n\n")
	b.WriteString("Read these pages with web_fetch; they're approved for this turn. Skip any that fail to load or don't help. " +
		"Then answer from what they say, citing the URLs you used, and say what's missing if they don't settle it. " +
		"Other pages, including links you find on these, need my approval.\n")
	for i, r := range links {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	if answer != "" {
		b.WriteString("\nThe search engine's own summary, unverified (check it against the pages):\n" + answer + "\n")
	}
	return b.String()
}

// SessionSearchPrompt asks the agent what this conversation said about
// terms, with the transcript passages that mention them (total is how
// many messages matched, of which matches are the ones sent).
func SessionSearchPrompt(terms string, matches []session.Match, total int) string {
	var b strings.Builder
	b.WriteString("Look back through this conversation for: " + terms + "\n\n")
	if total == 0 {
		b.WriteString("No message in the session transcript contains these words. Answer from what you remember of this conversation, and say plainly if it never came up. Don't use tools.")
		return b.String()
	}
	fmt.Fprintf(&b, "These passages from the session transcript mention it (%d of %d matching messages, oldest first). "+
		"Earlier turns may have been compacted, so some may no longer be in your context:\n", len(matches), total)
	for _, m := range matches {
		fmt.Fprintf(&b, "\n[message %d, %s, %s]\n%s\n", m.Index+1, m.Message.Role, m.Message.Timestamp.Format("2006-01-02 15:04"), m.Excerpt)
	}
	b.WriteString("\nSay what was said or decided about it, and when, quoting briefly. " +
		"Use tools only to check a file these passages point to. If none of it is relevant, say so.")
	return b.String()
}
