package client

import (
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"github.com/retail-cortex/code_puppy/internal/workers"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Conversions from the API's messages to internal/app's types, and from its
// errors back to app's typed errors, so front ends handle a remote
// workspace's results exactly like a local one's.

// sentinels are app's errors that carry no data, by ErrorInfo reason.
var sentinels = map[string]error{
	"NO_ACTIVE_SESSION":   app.ErrNoActiveSession,
	"SNAPSHOT_NAME_TAKEN": app.ErrSnapshotNameTaken,
	"BAD_MODEL_REF":       app.ErrBadModelRef,
	"INVALID_AGENCY":      app.ErrInvalidAgency,
	"UNDO_CONFLICT":       app.ErrUndoConflict,
	"SCRIPTS_DISABLED":    app.ErrScriptsDisabled,
	"UNKNOWN_LOCALE":      app.ErrUnknownLocale,
	"IMAGES_DISABLED":     app.ErrImagesDisabled,
	"NO_FETCH":            app.ErrNoFetch,
	"NO_SEARCH":           app.ErrNoSearch,
	"NOTHING_TO_COMPACT":  app.ErrNothingToCompact,
	"WORKSPACE_BUSY":      app.ErrWorkspaceBusy,
	"UNKNOWN_WORKER":      app.ErrUnknownWorker,
	"WORKER_DISABLED":     app.ErrWorkerNotEnabled,
	"RUN_IN_PROGRESS":     app.ErrRunInProgress,
	"WORKERS_DISABLED":    app.ErrWorkersDisabled,
	"HASH_MISMATCH":       workers.ErrHashMismatch,
}

// fromAPI turns a failed call's error into app's typed error for its
// reason, or an error with the service's message.
func fromAPI(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	for _, d := range ce.Details() {
		if v, derr := d.Value(); derr == nil {
			if info, ok := v.(*pb.ErrorInfo); ok {
				return errorFromInfo(info)
			}
		}
	}
	return errors.New(ce.Message())
}

// errorFromInfo is app's error for an ErrorInfo (nil for nil).
func errorFromInfo(info *pb.ErrorInfo) error {
	if info == nil {
		return nil
	}
	msg := errors.New(info.Message)
	switch info.Reason {
	case "UNKNOWN_AGENT":
		return &app.UnknownAgentError{Name: info.Metadata["name"]}
	case "RESUME_FAILED":
		return &app.ResumeError{Err: msg}
	case "INVALID_SETTING":
		return &app.InvalidSettingError{Err: msg}
	case "UNKNOWN_SETTING":
		return &app.UnknownSettingError{Key: info.Metadata["key"]}
	case "PROMPT_BLOCKED":
		return &app.BlockedError{Reason: info.Metadata["reason"]}
	}
	if s, ok := sentinels[info.Reason]; ok {
		// errors.Is finds the sentinel; the message stays the service's.
		return wrapped{msg: info.Message, sentinel: s}
	}
	return msg
}

// wrapped is a sentinel error with the service's message.
type wrapped struct {
	msg      string
	sentinel error
}

func (w wrapped) Error() string { return w.msg }
func (w wrapped) Unwrap() error { return w.sentinel }

func timeOf(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func session(s *pb.SessionInfo) app.SessionInfo {
	if s == nil {
		return app.SessionInfo{}
	}
	out := app.SessionInfo{
		ID: s.Id, Title: s.Title, Agent: s.Agent, Workspace: s.Workspace, Snapshot: s.Snapshot, From: s.From,
		MessageCount: int(s.MessageCount), Created: timeOf(s.Created), Updated: timeOf(s.Updated),
	}
	for _, m := range s.Messages {
		out.Messages = append(out.Messages, app.Message{Role: m.Role, Text: m.Text, Time: timeOf(m.Time)})
	}
	return out
}

func sessions(list []*pb.SessionInfo) []app.SessionInfo {
	out := make([]app.SessionInfo, len(list))
	for i, s := range list {
		out[i] = session(s)
	}
	return out
}

func agent(a *pb.AgentInfo) app.AgentInfo {
	return app.AgentInfo{Name: a.GetName(), DisplayName: a.GetDisplayName(), Description: a.GetDescription(), Active: a.GetActive(), PinnedModel: a.GetPinnedModel()}
}

func saved(s *pb.Saved) app.Saved {
	out := app.Saved{Path: s.GetPath()}
	if s.GetError() != "" {
		out.Err = errors.New(s.GetError())
	}
	return out
}

func usage(u *pb.Usage) app.Usage {
	if u == nil {
		return app.Usage{}
	}
	return app.Usage{
		Calls: int(u.Calls), Input: u.Input, Cached: u.Cached, CacheWrite: u.CacheWrite, Output: u.Output,
		LastPrompt: u.LastPrompt, CostUSD: u.CostUsd, Priced: u.Priced,
	}
}

func modelSettings(s *pb.ModelSettings) config.ModelSettings {
	out := config.ModelSettings{Temperature: s.Temperature, TopP: s.TopP}
	if s.MaxTokens != nil {
		v := int(*s.MaxTokens)
		out.MaxTokens = &v
	}
	if s.Seed != nil {
		v := int(*s.Seed)
		out.Seed = &v
	}
	return out
}

func modelSettingsInfo(m *pb.ModelSettingsInfo) app.ModelSettingsInfo {
	if m == nil {
		return app.ModelSettingsInfo{}
	}
	return app.ModelSettingsInfo{
		Model: m.Model, Provider: m.Provider, Settings: modelSettings(m.GetSettings()),
		GlobalTemperature: m.GlobalTemperature, GlobalMaxTokens: int(m.GlobalMaxTokens),
	}
}

func skill(s *pb.SkillInfo) app.SkillInfo {
	out := app.SkillInfo{
		Name: s.Name, Description: s.Description, Version: s.Version, License: s.License, Category: s.Category,
		Compatibility: s.Compatibility, Tags: s.Tags, Hash: s.Hash, Tier: s.Tier, Bypass: s.Bypass,
		Network: s.Network, NeedsNetwork: s.NeedsNetwork, Env: s.Env, Withheld: s.Withheld, Blocked: s.Blocked,
	}
	for _, t := range s.Tools {
		out.Tools = append(out.Tools, app.SkillTool{Name: t.Name, Scopes: t.Scopes, Why: t.Why})
	}
	for _, sc := range s.Scripts {
		out.Scripts = append(out.Scripts, app.SkillScript{
			Name: sc.Name, Language: sc.Language, Source: sc.Source, Timeout: sc.GetTimeout().AsDuration(),
			Deps: sc.Deps, Allowed: sc.Allowed, Reasons: sc.Reasons,
		})
	}
	return out
}

// image is an image held by the service: its data stays there.
func imageOf(m *pb.Image) *images.Image {
	return &images.Image{
		Name: m.Name, MIME: m.MimeType, Width: int(m.Width), Height: int(m.Height), SHA256: m.Id,
		Resized: m.Resized, Size: int(m.Size),
	}
}

// event converts a model event (text, tool call or result).
func event(ev *pb.TurnEvent) (app.Event, bool) {
	out := app.Event{Author: ev.Author}
	switch k := ev.Kind.(type) {
	case *pb.TurnEvent_Text:
		out.Text = &app.Text{Text: k.Text.Text, Partial: k.Text.Partial, Repeat: k.Text.Repeat, Thought: k.Text.Thought}
	case *pb.TurnEvent_ToolCall:
		out.ToolCall = &app.ToolCall{ID: k.ToolCall.Id, Name: k.ToolCall.Name, Args: k.ToolCall.GetArgs().AsMap(), Partial: k.ToolCall.Partial}
	case *pb.TurnEvent_ToolResult:
		out.ToolResult = &app.ToolResult{ID: k.ToolResult.Id, Name: k.ToolResult.Name, Result: k.ToolResult.GetResult().AsMap()}
	default:
		return app.Event{}, false
	}
	return out, true
}

func actionKind(k pb.ActionKind) tools.ActionKind {
	switch k {
	case pb.ActionKind_ACTION_KIND_COMMAND:
		return tools.ActionCommand
	case pb.ActionKind_ACTION_KIND_WRITE:
		return tools.ActionWrite
	case pb.ActionKind_ACTION_KIND_DELETE:
		return tools.ActionDelete
	case pb.ActionKind_ACTION_KIND_NETWORK:
		return tools.ActionNetwork
	case pb.ActionKind_ACTION_KIND_MCP:
		return tools.ActionMCP
	}
	return ""
}

func decisionMsg(d tools.Decision) pb.Decision {
	switch d {
	case tools.DecisionOnce:
		return pb.Decision_DECISION_ONCE
	case tools.DecisionSession:
		return pb.Decision_DECISION_SESSION
	case tools.DecisionAlways:
		return pb.Decision_DECISION_ALWAYS
	}
	return pb.Decision_DECISION_DENY
}
