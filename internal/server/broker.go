package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"connectrpc.com/connect"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

// The broker turns the agent's approval requests and questions, which the
// engine asks through callbacks, into events on the running turn's stream,
// and waits for the client's Approve or Answer.

// sinkKey carries a running turn's event sender in its context.
type sinkKey struct{}

func withSink(ctx context.Context, send func(*pb.TurnEvent)) context.Context {
	return context.WithValue(ctx, sinkKey{}, send)
}

// errNoClient refuses a request made outside a turn with a client (e.g. by
// a worker), where nobody can answer.
var errNoClient = errors.New("no client is attached to answer")

// reply is a client's answer: a decision or text.
type reply struct {
	decision tools.Decision
	text     string
}

// broker keeps the requests waiting for an answer, by ID.
type broker struct {
	mu      sync.Mutex
	pending map[string]chan reply
}

func newBroker() *broker { return &broker{pending: map[string]chan reply{}} }

// ask sends the event built for a new request ID on the turn's stream and
// waits for the answer, or for the turn to end.
func (b *broker) ask(ctx context.Context, event func(id string) *pb.TurnEvent) (reply, error) {
	send, ok := ctx.Value(sinkKey{}).(func(*pb.TurnEvent))
	if !ok {
		return reply{}, errNoClient
	}
	id := newID()
	ch := make(chan reply, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()
	send(event(id))
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return reply{}, ctx.Err()
	}
}

// answer delivers a reply to the request with the ID.
func (b *broker) answer(id string, r reply) error {
	b.mu.Lock()
	ch, ok := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if !ok {
		return apiError(connect.CodeNotFound, "UNKNOWN_REQUEST", fmt.Errorf("no request %q is waiting (answered, or its turn ended)", id), "request_id", id)
	}
	ch <- r
	return nil
}

// approve is the workspaces' tools.Approver.
func (b *broker) approve(ctx context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
	r, err := b.ask(ctx, func(id string) *pb.TurnEvent {
		return &pb.TurnEvent{Kind: &pb.TurnEvent_ApprovalRequest{ApprovalRequest: &pb.ApprovalRequest{
			RequestId: id, Tool: req.Tool, Kind: actionKind(req.Kind), Detail: req.Detail, Diff: req.Diff, ScopeLabel: req.KeyLabel,
		}}}
	})
	if err != nil {
		return tools.DecisionDeny, err
	}
	return r.decision, nil
}

// question is the workspaces' tools.UserPromptFunc.
func (b *broker) question(ctx context.Context, question string, options []string) (string, error) {
	r, err := b.ask(ctx, func(id string) *pb.TurnEvent {
		return &pb.TurnEvent{Kind: &pb.TurnEvent_Question{Question: &pb.Question{RequestId: id, Question: question, Options: options}}}
	})
	return r.text, err
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
