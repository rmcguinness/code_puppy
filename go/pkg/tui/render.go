package tui

import (
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/textutil"
)

// ANSI color codes
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	BgBlack = "\033[40m"
	BgBlue  = "\033[44m"
)

// PrintBanner renders the Code Puppy ASCII splash banner.
func PrintBanner(version, agent, model string) {
	banner := `
  __      _
o'')}____//      __ _ _ _  _ _ __ _  _
 ` + "`" + `_/      )     / _/ _ \ || | '_ \ || |
 (_(_/-(_/     \__\___/\_,_| .__/\_, |
                            |_|   |__/
`
	fmt.Printf("%s%s%s", Cyan, banner, Reset)
	fmt.Printf("🐶 %sCode Puppy Go%s (Google ADK Edition) %sv%s%s\n", Bold, Reset, Yellow, version, Reset)
	fmt.Printf("🐕 Active Agent: %s%s%s | Model: %s%s%s\n", Green, agent, Reset, Blue, model, Reset)
	fmt.Printf("💡 Type %s/help%s for commands or ask anything. Press %sCtrl+C%s to exit.\n\n", Bold, Reset, Dim, Reset)
}

// FormatDiff highlights diff additions in green and deletions in red.
func FormatDiff(diffText string) string {
	lines := strings.Split(diffText, "\n")
	var sb strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			sb.WriteString(Green + line + Reset + "\n")
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			sb.WriteString(Red + line + Reset + "\n")
		} else if strings.HasPrefix(line, "@@") {
			sb.WriteString(Cyan + line + Reset + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	return sb.String()
}

// safe prepares untrusted text (model output, tool args/results) for the
// terminal by removing control sequences.
func safe(s string) string { return textutil.SanitizeTerminal(s) }

// PrintModelText writes streamed model text with control sequences removed.
func PrintModelText(text string) { fmt.Print(safe(text)) }

// FormatToolCall renders an invocation badge for a tool.
func FormatToolCall(toolName string, args map[string]any) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s⚙️  Tool Call:%s %s%s%s", Yellow, Reset, Bold, safe(toolName), Reset)
	if path, ok := args["path"].(string); ok && path != "" {
		fmt.Fprintf(&sb, " (%s%s%s)", Cyan, safe(textutil.Ellipsize(path, 120)), Reset)
	} else if cmd, ok := args["command"].(string); ok && cmd != "" {
		fmt.Fprintf(&sb, " (%s%s%s)", Dim, safe(textutil.Ellipsize(cmd, 60)), Reset)
	} else if q, ok := args["query"].(string); ok && q != "" {
		fmt.Fprintf(&sb, " (query: %s%s%s)", Cyan, safe(textutil.Ellipsize(q, 80)), Reset)
	}
	sb.WriteByte('\n')
	return sb.String()
}

// PrintToolCall prints FormatToolCall.
func PrintToolCall(toolName string, args map[string]any) {
	fmt.Print(FormatToolCall(toolName, args))
}

// FormatToolResult renders a completed tool badge.
func FormatToolResult(toolName string, success bool, summary string) string {
	icon := "✅"
	color := Green
	if !success {
		icon = "❌"
		color = Red
	}
	toolName = safe(toolName)
	if summary != "" {
		// Collapse to one line so multi-line output cannot spoof other UI.
		summary = strings.Join(strings.Fields(safe(summary)), " ")
		return fmt.Sprintf("%s%s [%s]:%s %s\n", color, icon, toolName, Reset, textutil.Ellipsize(summary, 80))
	}
	return fmt.Sprintf("%s%s [%s] done%s\n", color, icon, toolName, Reset)
}

// PrintToolResult prints FormatToolResult.
func PrintToolResult(toolName string, success bool, summary string) {
	fmt.Print(FormatToolResult(toolName, success, summary))
}

// SummarizeToolResponse picks a short summary from a function response and
// reports whether it represents success.
func SummarizeToolResponse(resp map[string]any) (summary string, success bool) {
	if resp == nil {
		return "", true
	}
	if errStr, ok := resp["error"].(string); ok && errStr != "" {
		return errStr, false
	}
	if resStr, ok := resp["result"].(string); ok && resStr != "" {
		return resStr, true
	}
	if cnt, ok := resp["content"].(string); ok && cnt != "" {
		return fmt.Sprintf("%d bytes read", len(cnt)), true
	}
	return "", true
}
