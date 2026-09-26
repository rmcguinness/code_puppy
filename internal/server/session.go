package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/images"
)

// sessionService implements SessionService.
type sessionService struct{ s *Server }

func (h sessionService) ListSessions(ctx context.Context, r req[pb.ListSessionsRequest]) (*connect.Response[pb.ListSessionsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	list, err := w.ListSessions(r.Msg.All)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.ListSessionsResponse{Sessions: sessionMsgs(list)})
}

func (h sessionService) GetActiveSession(ctx context.Context, r req[pb.GetActiveSessionRequest]) (*connect.Response[pb.GetActiveSessionResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	out := &pb.GetActiveSessionResponse{}
	if s, found := w.ActiveSession(); found {
		out.Session = sessionMsg(s)
	}
	return ok(out)
}

func (h sessionService) NewSession(ctx context.Context, r req[pb.NewSessionRequest]) (*connect.Response[pb.NewSessionResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	s, err := w.NewSession()
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.NewSessionResponse{Session: sessionMsg(s)})
}

func (h sessionService) OpenSession(ctx context.Context, r req[pb.OpenSessionRequest]) (*connect.Response[pb.OpenSessionResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	s, resumed, err := w.OpenSession(r.Msg.Resume, r.Msg.ContinueLatest)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.OpenSessionResponse{Session: sessionMsg(s), Resumed: resumed})
}

func (h sessionService) LoadSession(ctx context.Context, r req[pb.LoadSessionRequest]) (*connect.Response[pb.LoadSessionResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	s, branched, err := w.LoadSession(r.Msg.Ref)
	if err != nil {
		return nil, apiError(connect.CodeNotFound, "SESSION_NOT_FOUND", err, "ref", r.Msg.Ref)
	}
	return ok(&pb.LoadSessionResponse{Session: sessionMsg(s), Branched: branched})
}

func (h sessionService) SaveSnapshot(ctx context.Context, r req[pb.SaveSnapshotRequest]) (*connect.Response[pb.SaveSnapshotResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	s, err := w.SaveSnapshot(r.Msg.Name, r.Msg.Force)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.SaveSnapshotResponse{Snapshot: sessionMsg(s)})
}

func (h sessionService) RenameSession(ctx context.Context, r req[pb.RenameSessionRequest]) (*connect.Response[pb.RenameSessionResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	s, err := w.RenameSession(r.Msg.Title)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.RenameSessionResponse{Session: sessionMsg(s)})
}

// RunTurn runs a turn and streams its events. Events are sent from one
// goroutine at a time (the send lock), since the agent and approval
// requests can produce them concurrently.
func (h sessionService) RunTurn(ctx context.Context, r req[pb.RunTurnRequest], stream *connect.ServerStream[pb.RunTurnResponse]) error {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return err
	}
	t := r.Msg.Turn
	if t == nil {
		return apiError(connect.CodeInvalidArgument, "INVALID_TURN", errors.New("turn is required"))
	}
	var imgs []*images.Image
	for _, id := range t.ImageIds {
		img, err := w.image(id)
		if err != nil {
			return err
		}
		imgs = append(imgs, img)
	}

	var mu sync.Mutex
	var sendErr error
	send := func(ev *pb.TurnEvent) {
		mu.Lock()
		defer mu.Unlock()
		if sendErr == nil {
			sendErr = stream.Send(&pb.RunTurnResponse{Event: ev})
		}
	}
	ctx = withSink(ctx, send)
	res, runErr := w.Run(ctx, r.Msg.SessionId, app.Turn{
		Text: t.Text, Prompt: t.Prompt, Plan: t.Plan, ReadOnly: t.ReadOnly, Aside: t.Aside, Accepted: t.Accepted,
		Images: imgs, MaxTurns: int(t.MaxTurns), FetchGrants: t.FetchGrants,
		OnAccepted: func() { send(&pb.TurnEvent{Kind: &pb.TurnEvent_Accepted{Accepted: &pb.Accepted{}}}) },
	}, func(e app.Event) { send(eventMsg(e)) })

	send(&pb.TurnEvent{Kind: &pb.TurnEvent_Finished{Finished: &pb.TurnFinished{
		Output: res.Output, Before: usageMsg(res.Before), After: usageMsg(res.After), Leftover: res.Leftover, Error: errorInfo(runErr),
	}}})
	if sendErr != nil {
		return fmt.Errorf("sending turn events: %w", sendErr)
	}
	return nil
}

func (h sessionService) Steer(ctx context.Context, r req[pb.SteerRequest]) (*connect.Response[pb.SteerResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	if err := w.Steer(ctx, r.Msg.SessionId, r.Msg.Text); err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.SteerResponse{})
}

func (h sessionService) GetUsage(ctx context.Context, r req[pb.GetUsageRequest]) (*connect.Response[pb.GetUsageResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	u, err := w.SessionUsage()
	if err != nil {
		return nil, toAPI(err)
	}
	c, err := w.Context()
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.GetUsageResponse{Usage: usageMsg(u), AutoCompact: c.AutoCompact, Threshold: int32(c.Threshold), Keep: int32(c.Keep)})
}

func (h sessionService) Compact(ctx context.Context, r req[pb.CompactRequest]) (*connect.Response[pb.CompactResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	res, err := w.Compact(ctx, r.Msg.Focus)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.CompactResponse{EventsCompacted: int32(res.EventsCompacted), SummaryChars: int32(res.SummaryChars), Before: usageMsg(res.Before), After: usageMsg(res.After)})
}

func (h sessionService) SearchSession(ctx context.Context, r req[pb.SearchSessionRequest]) (*connect.Response[pb.SearchSessionResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	found, prompt := w.SearchSession(r.Msg.Terms)
	return ok(&pb.SearchSessionResponse{Found: int32(found), Prompt: prompt})
}

func (h sessionService) Approve(_ context.Context, r req[pb.ApproveRequest]) (*connect.Response[pb.ApproveResponse], error) {
	if r.Msg.Decision == pb.Decision_DECISION_UNSPECIFIED {
		return nil, apiError(connect.CodeInvalidArgument, "INVALID_DECISION", errors.New("decision is required"))
	}
	if err := h.s.broker.answer(r.Msg.RequestId, reply{decision: decision(r.Msg.Decision)}); err != nil {
		return nil, err
	}
	return ok(&pb.ApproveResponse{})
}

func (h sessionService) Answer(_ context.Context, r req[pb.AnswerRequest]) (*connect.Response[pb.AnswerResponse], error) {
	if err := h.s.broker.answer(r.Msg.RequestId, reply{text: r.Msg.Answer}); err != nil {
		return nil, err
	}
	return ok(&pb.AnswerResponse{})
}
