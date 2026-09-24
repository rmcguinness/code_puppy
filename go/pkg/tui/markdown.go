package tui

import (
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/glamour"
)

// markdownStream renders streamed Markdown block by block: text is buffered
// until a block is complete (a blank line outside a code fence, or a closing
// fence), then that block is rendered, so output appears progressively
// without re-rendering what was already printed.
type markdownStream struct {
	r       *glamour.TermRenderer
	out     io.Writer
	pending strings.Builder
}

func newMarkdownStream(out io.Writer, theme string, width int) (*markdownStream, error) {
	opts := []glamour.TermRendererOption{glamour.WithEmoji()}
	if theme == "" || theme == "auto" {
		// glamour's own auto-detection queries the terminal, which would read
		// from stdin underneath the line editor; use the environment instead.
		theme = themeFromEnv()
	}
	opts = append(opts, glamour.WithStandardStyle(theme))
	if width > 20 {
		opts = append(opts, glamour.WithWordWrap(min(width-4, 120)))
	}
	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return nil, err
	}
	return &markdownStream{r: r, out: out}, nil
}

// themeFromEnv picks "light" when COLORFGBG reports a light background
// (e.g. "0;15"), else "dark".
func themeFromEnv() string {
	v := os.Getenv("COLORFGBG")
	if i := strings.LastIndexByte(v, ';'); i >= 0 {
		switch v[i+1:] {
		case "7", "15":
			return "light"
		}
	}
	return "dark"
}

// Write adds streamed text and renders any completed blocks.
func (m *markdownStream) Write(text string) {
	m.pending.WriteString(text)
	buf := m.pending.String()
	if cut := completeBlocks(buf); cut > 0 {
		m.render(buf[:cut])
		m.pending.Reset()
		m.pending.WriteString(buf[cut:])
	}
}

// Flush renders whatever remains.
func (m *markdownStream) Flush() {
	if m.pending.Len() > 0 {
		m.render(m.pending.String())
		m.pending.Reset()
	}
}

func (m *markdownStream) render(block string) {
	if strings.TrimSpace(block) == "" {
		return
	}
	out, err := m.r.Render(block)
	if err != nil {
		io.WriteString(m.out, block)
		return
	}
	io.WriteString(m.out, strings.TrimLeft(strings.TrimRight(out, "\n "), "\n")+"\n")
}

// completeBlocks returns the length of the longest prefix of s made of whole
// Markdown blocks, or 0 if no block is complete yet.
func completeBlocks(s string) int {
	cut, inFence, pos := 0, false, 0
	prevBlank := false
	for {
		nl := strings.IndexByte(s[pos:], '\n')
		if nl < 0 {
			return cut
		}
		line := s[pos : pos+nl]
		end := pos + nl + 1
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			if inFence {
				cut = end // a closed fence ends a block
			}
			inFence = !inFence
		case !inFence && trimmed == "" && !prevBlank:
			cut = end
		}
		prevBlank = trimmed == ""
		pos = end
	}
}
