package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/genai"
)

// searchApp is a REPL app with web access, a SearXNG-style search server
// returning pages served locally, and no auto-approval: only the pages
// /search web hands over may be fetched without asking.
func searchApp(t *testing.T, input string, replies ...*genai.Content) (*App, *runtime.MockLLM, string) {
	t.Helper()
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "contents of %s", r.URL.Path)
	}))
	t.Cleanup(pages.Close)
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var results []string
		for _, p := range []string{"/one", "/one#dup", "/manual.pdf", "/two", "/three", "/four", "/five", "/six"} {
			results = append(results, fmt.Sprintf(`{"title":"Page %s","url":"%s%s","content":"about %s"}`, p, pages.URL, p, p))
		}
		io.WriteString(w, `{"results":[`+strings.Join(results, ",")+`]}`)
	}))
	t.Cleanup(search.Close)

	isolateHome(t)
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Images.Dir = t.TempDir()
	cfg.Audit.Enabled = false
	cfg.CodePuppy.AutoApprove = false
	cfg.Tools.ApprovalsFile = ""
	cfg.Sandbox.AllowNetwork = true
	cfg.Web.Enabled, cfg.Web.AllowPrivate = true, true
	cfg.Web.SearchProvider, cfg.Web.SearchURL = "searxng", search.URL
	llm := runtime.NewMockLLM("gemini-3.8-flash", replies...)
	app := openApp(t, cfg, llm)
	app.Input = NewLineReader(strings.NewReader(input), io.Discard)
	return app, llm, pages.URL
}

// toolResults collects the function responses the model was sent, by tool.
func toolResults(llm *runtime.MockLLM) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	last := llm.Requests[len(llm.Requests)-1]
	for _, c := range last.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				out[p.FunctionResponse.Name] = append(out[p.FunctionResponse.Name], p.FunctionResponse.Response)
			}
		}
	}
	return out
}

func TestSearchWebHandsFiveReadableLinksToTheAgent(t *testing.T) {
	replies := []*genai.Content{
		toolCallContent("web_fetch", map[string]any{"url": "PAGES/two"}),                // handed over: no prompt
		toolCallContent("web_fetch", map[string]any{"url": "PAGES/six"}),                // not handed over: needs approval
		toolCallContent("create_file", map[string]any{"path": "x.txt", "content": "x"}), // read-only turn
		genai.NewContentFromText("Pages two says so.", genai.RoleModel),
	}
	app, llm, pages := searchApp(t, "/search\n/search web golang errors\n/exit\n", replies...)
	for _, r := range replies[:2] {
		r.Parts[0].FunctionCall.Args["url"] = strings.Replace(r.Parts[0].FunctionCall.Args["url"].(string), "PAGES", pages, 1)
	}
	out := captureStdout(t, func() { RunREPL(context.Background(), app) })

	if !strings.Contains(out, "Usage: /search web <terms>") {
		t.Errorf("usage:\n%s", out)
	}
	if !strings.Contains(out, "Searching searxng for golang errors") || !strings.Contains(out, "5. Page /five") || strings.Contains(out, "manual.pdf") {
		t.Errorf("results shown:\n%s", out)
	}
	prompt := llm.Requests[0].Contents[len(llm.Requests[0].Contents)-1].Parts[0].Text
	for _, want := range []string{"I searched the web for: golang errors", pages + "/one", pages + "/five", "about /two"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "/six") || strings.Contains(prompt, "manual.pdf") || strings.Count(prompt, pages+"/one") != 1 {
		t.Errorf("prompt has links it shouldn't:\n%s", prompt)
	}

	res := toolResults(llm)
	if fetches := res["web_fetch"]; len(fetches) != 2 || fetches[0]["content"] != "contents of /two" || !strings.Contains(fmt.Sprint(fetches[1]["error"]), "approv") {
		t.Errorf("web_fetch results: %v", fetches)
	}
	if cf := res["create_file"]; len(cf) != 1 || !strings.Contains(fmt.Sprint(cf[0]["error"]), "search is read-only") {
		t.Errorf("create_file result: %v", cf)
	}
	msgs := app.Workspace.Storage().Active().Messages
	if len(msgs) < 1 || msgs[0].Content != "/search web golang errors" {
		t.Errorf("transcript: %+v", msgs)
	}
}

func TestSearchSessionSendsMatchingPassages(t *testing.T) {
	app, llm, _ := searchApp(t, "/search session pineapple\n/search session mango\n/exit\n",
		genai.NewContentFromText("You chose pineapple on day one.", genai.RoleModel),
		genai.NewContentFromText("Mango never came up.", genai.RoleModel))
	st := app.Workspace.Storage()
	st.CreateSession("", "t", "code-puppy")
	st.AddMessage("user", "let's pick a fruit")
	st.AddMessage("model", "I suggest PINEAPPLE for the demo")
	st.AddMessage("user", "ok")

	out := captureStdout(t, func() { RunREPL(context.Background(), app) })
	if !strings.Contains(out, "Found 1 message about it in this session") || !strings.Contains(out, "Nothing in this session's transcript mentions it") {
		t.Errorf("output:\n%s", out)
	}
	first := llm.Requests[0].Contents[len(llm.Requests[0].Contents)-1].Parts[0].Text
	if !strings.Contains(first, "Look back through this conversation for: pineapple") || !strings.Contains(first, "[message 2, model,") || !strings.Contains(first, "I suggest PINEAPPLE") {
		t.Errorf("first prompt:\n%s", first)
	}
	second := llm.Requests[1].Contents[len(llm.Requests[1].Contents)-1].Parts[0].Text
	if !strings.Contains(second, "No message in the session transcript contains these words") {
		t.Errorf("second prompt:\n%s", second)
	}
}
