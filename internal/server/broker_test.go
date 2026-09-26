package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/config"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"google.golang.org/genai"
)

func call(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

// runTurn runs a turn, answering each approval request with decide and each
// question with answer, and returns the tool results and the final event.
func runTurn(t *testing.T, c clients, dir, prompt string, decide pb.Decision, answer string) ([]*pb.ToolResult, *pb.TurnFinished) {
	t.Helper()
	ctx := context.Background()
	s, err := c.sessions.NewSession(ctx, connect.NewRequest(&pb.NewSessionRequest{Workspace: dir}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := c.sessions.RunTurn(ctx, connect.NewRequest(&pb.RunTurnRequest{Workspace: dir, SessionId: s.Msg.Session.Id, Turn: &pb.Turn{Text: prompt}}))
	if err != nil {
		t.Fatal(err)
	}
	var results []*pb.ToolResult
	var finished *pb.TurnFinished
	for stream.Receive() {
		ev := stream.Msg().Event
		switch {
		case ev.GetApprovalRequest() != nil:
			ar := ev.GetApprovalRequest()
			if ar.Kind != pb.ActionKind_ACTION_KIND_WRITE || ar.Detail == "" || ar.RequestId == "" {
				t.Errorf("approval request %v", ar)
			}
			if _, err := c.sessions.Approve(ctx, connect.NewRequest(&pb.ApproveRequest{Workspace: dir, RequestId: ar.RequestId, Decision: decide})); err != nil {
				t.Errorf("approve: %v", err)
			}
		case ev.GetQuestion() != nil:
			q := ev.GetQuestion()
			if _, err := c.sessions.Answer(ctx, connect.NewRequest(&pb.AnswerRequest{Workspace: dir, RequestId: q.RequestId, Answer: answer})); err != nil {
				t.Errorf("answer: %v", err)
			}
		case ev.GetToolResult() != nil:
			results = append(results, ev.GetToolResult())
		case ev.GetFinished() != nil:
			finished = ev.GetFinished()
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	return results, finished
}

func TestApprovalsTravelOverTheStream(t *testing.T) {
	create := call("create_file", map[string]any{"path": "made.txt", "content": "hi\n"})
	c, _ := serve(t, func(cfg *config.Config) { cfg.CodePuppy.AutoApprove = false }, create, text("done"), create, text("done"))
	dir := t.TempDir()

	// Approved once: the file is written.
	if _, fin := runTurn(t, c, dir, "make a file", pb.Decision_DECISION_ONCE, ""); fin == nil || fin.Error != nil {
		t.Fatalf("finished %v", fin)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Fatalf("approved write didn't happen: %v", err)
	}
	os.Remove(filepath.Join(dir, "made.txt"))

	// Denied: the tool reports the refusal and nothing is written.
	results, _ := runTurn(t, c, dir, "make it again", pb.Decision_DECISION_DENY, "")
	if len(results) != 1 {
		t.Fatalf("results %v", results)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); !os.IsNotExist(err) {
		t.Error("denied write happened")
	}
}

func TestQuestionsTravelOverTheStream(t *testing.T) {
	ask := call("ask_user_question", map[string]any{"question": "Tabs or spaces?", "options": []any{"tabs", "spaces"}})
	c, _ := serve(t, nil, ask, text("ok"))
	results, _ := runTurn(t, c, t.TempDir(), "ask me", pb.Decision_DECISION_UNSPECIFIED, "tabs")
	if len(results) != 1 || results[0].Result.AsMap()["answer"] != "tabs" {
		t.Errorf("results %v", results)
	}
}

func TestAnsweringAnUnknownRequestFails(t *testing.T) {
	c, _ := serve(t, nil)
	_, err := c.sessions.Approve(context.Background(), connect.NewRequest(&pb.ApproveRequest{Workspace: t.TempDir(), RequestId: "nope", Decision: pb.Decision_DECISION_ONCE}))
	if code, info := errorReason(t, err); code != connect.CodeNotFound || info.Reason != "UNKNOWN_REQUEST" {
		t.Errorf("%v %v", code, info)
	}
	_, err = c.sessions.Approve(context.Background(), connect.NewRequest(&pb.ApproveRequest{RequestId: "nope"}))
	if code, info := errorReason(t, err); code != connect.CodeInvalidArgument || info.Reason != "INVALID_DECISION" {
		t.Errorf("%v %v", code, info)
	}
}

// Outside a turn nobody can answer, so a request is refused at once.
func TestRequestsWithoutAClientAreRefused(t *testing.T) {
	b := newBroker()
	if _, err := b.question(context.Background(), "q", nil); err != errNoClient {
		t.Errorf("err = %v", err)
	}
}

// A client that goes away while an approval waits ends the turn; nothing
// stays pending.
func TestCancellingWhileAnApprovalWaits(t *testing.T) {
	create := call("create_file", map[string]any{"path": "made.txt", "content": "hi\n"})
	c, s := serve(t, func(cfg *config.Config) { cfg.CodePuppy.AutoApprove = false }, create, text("done"))
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, _ := c.sessions.NewSession(ctx, connect.NewRequest(&pb.NewSessionRequest{Workspace: dir}))
	stream, err := c.sessions.RunTurn(ctx, connect.NewRequest(&pb.RunTurnRequest{Workspace: dir, SessionId: sess.Msg.Session.Id, Turn: &pb.Turn{Text: "make a file"}}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if stream.Msg().Event.GetApprovalRequest() != nil {
			cancel()
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.broker.mu.Lock()
		n := len(s.broker.pending)
		s.broker.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests still pending", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); !os.IsNotExist(err) {
		t.Error("the write happened without approval")
	}
}
