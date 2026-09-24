package tui

import (
	"fmt"
	"strings"
)

// ANSI color codes
const (
	Reset     = "\033[0m"
	Bold      = "\033[1m"
	Dim       = "\033[2m"
	Red       = "\033[31m"
	Green     = "\033[32m"
	Yellow    = "\033[33m"
	Blue      = "\033[34m"
	Magenta   = "\033[35m"
	Cyan      = "\033[36m"
	White     = "\033[37m"
	BgBlack   = "\033[40m"
	BgBlue    = "\033[44m"
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

// PrintToolCall renders an invocation badge for a tool.
func PrintToolCall(toolName string, args map[string]any) {
	fmt.Printf("\n%s⚙️  Tool Call:%s %s%s%s", Yellow, Reset, Bold, toolName, Reset)
	if path, ok := args["path"].(string); ok && path != "" {
		fmt.Printf(" (%s%s%s)", Cyan, path, Reset)
	} else if cmd, ok := args["command"].(string); ok && cmd != "" {
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		fmt.Printf(" (%s%s%s)", Dim, cmd, Reset)
	} else if q, ok := args["query"].(string); ok && q != "" {
		fmt.Printf(" (query: %s%s%s)", Cyan, q, Reset)
	}
	fmt.Println()
}

// PrintToolResult renders a completed tool badge.
func PrintToolResult(toolName string, success bool, summary string) {
	icon := "✅"
	color := Green
	if !success {
		icon = "❌"
		color = Red
	}
	if summary != "" {
		if len(summary) > 80 {
			summary = summary[:77] + "..."
		}
		fmt.Printf("%s%s [%s]:%s %s\n", color, icon, toolName, Reset, summary)
	} else {
		fmt.Printf("%s%s [%s] done%s\n", color, icon, toolName, Reset)
	}
}
