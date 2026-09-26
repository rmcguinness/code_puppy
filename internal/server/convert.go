package server

import (
	"time"

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Conversions from internal/app's types to the API's messages.

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func sessionMsg(s app.SessionInfo) *pb.SessionInfo {
	out := &pb.SessionInfo{
		Id: s.ID, Title: s.Title, Agent: s.Agent, Workspace: s.Workspace, Snapshot: s.Snapshot, From: s.From,
		MessageCount: int32(s.MessageCount), Created: timestamp(s.Created), Updated: timestamp(s.Updated),
	}
	for _, m := range s.Messages {
		out.Messages = append(out.Messages, &pb.Message{Role: m.Role, Text: m.Text, Time: timestamp(m.Time)})
	}
	return out
}

func sessionMsgs(list []app.SessionInfo) []*pb.SessionInfo {
	out := make([]*pb.SessionInfo, len(list))
	for i, s := range list {
		out[i] = sessionMsg(s)
	}
	return out
}

func usageMsg(u app.Usage) *pb.Usage {
	return &pb.Usage{
		Calls: int32(u.Calls), Input: u.Input, Cached: u.Cached, CacheWrite: u.CacheWrite, Output: u.Output,
		LastPrompt: u.LastPrompt, CostUsd: u.CostUSD, Priced: u.Priced,
	}
}

func savedMsg(s app.Saved) *pb.Saved {
	out := &pb.Saved{Path: s.Path}
	if s.Err != nil {
		out.Error = s.Err.Error()
	}
	return out
}

func agentMsg(a app.AgentInfo) *pb.AgentInfo {
	return &pb.AgentInfo{Name: a.Name, DisplayName: a.DisplayName, Description: a.Description, Active: a.Active, PinnedModel: a.PinnedModel}
}

func modelSettingsMsg(s config.ModelSettings) *pb.ModelSettings {
	out := &pb.ModelSettings{Temperature: s.Temperature, TopP: s.TopP}
	if s.MaxTokens != nil {
		v := int32(*s.MaxTokens)
		out.MaxTokens = &v
	}
	if s.Seed != nil {
		v := int32(*s.Seed)
		out.Seed = &v
	}
	return out
}

func modelSettingsInfoMsg(i app.ModelSettingsInfo) *pb.ModelSettingsInfo {
	return &pb.ModelSettingsInfo{
		Model: i.Model, Provider: i.Provider, Settings: modelSettingsMsg(i.Settings),
		GlobalTemperature: i.GlobalTemperature, GlobalMaxTokens: int32(i.GlobalMaxTokens),
	}
}

func skillMsg(s app.SkillInfo) *pb.SkillInfo {
	out := &pb.SkillInfo{
		Name: s.Name, Description: s.Description, Version: s.Version, License: s.License, Category: s.Category,
		Compatibility: s.Compatibility, Tags: s.Tags, Hash: s.Hash, Tier: s.Tier, Bypass: s.Bypass,
		Network: s.Network, NeedsNetwork: s.NeedsNetwork, Env: s.Env, Withheld: s.Withheld, Blocked: s.Blocked,
	}
	for _, t := range s.Tools {
		out.Tools = append(out.Tools, &pb.SkillTool{Name: t.Name, Scopes: t.Scopes, Why: t.Why})
	}
	for _, sc := range s.Scripts {
		out.Scripts = append(out.Scripts, &pb.SkillScript{
			Name: sc.Name, Language: sc.Language, Source: sc.Source, Timeout: durationpb.New(sc.Timeout),
			Deps: sc.Deps, Allowed: sc.Allowed, Reasons: sc.Reasons,
		})
	}
	return out
}

func skillMsgs(list []app.SkillInfo) []*pb.SkillInfo {
	out := make([]*pb.SkillInfo, len(list))
	for i, s := range list {
		out[i] = skillMsg(s)
	}
	return out
}

func imageMsg(img *images.Image) *pb.Image {
	return &pb.Image{
		Id: img.SHA256, Name: img.Name, MimeType: img.MIME, Width: int32(img.Width), Height: int32(img.Height),
		Size: int64(len(img.Data)), Resized: img.Resized,
	}
}

func approvalMsg(a app.Approval) *pb.Approval {
	return &pb.Approval{Key: a.Key, Kind: a.Kind, Subject: a.Subject, Dir: a.Dir, Always: a.Always, Added: timestamp(a.Added)}
}

// structMsg converts tool arguments or results; values JSON can't carry
// are dropped rather than failing the event.
func structMsg(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

func eventMsg(e app.Event) *pb.TurnEvent {
	out := &pb.TurnEvent{Author: e.Author}
	switch {
	case e.Text != nil:
		out.Kind = &pb.TurnEvent_Text{Text: &pb.Text{Text: e.Text.Text, Partial: e.Text.Partial, Repeat: e.Text.Repeat, Thought: e.Text.Thought}}
	case e.ToolCall != nil:
		out.Kind = &pb.TurnEvent_ToolCall{ToolCall: &pb.ToolCall{Id: e.ToolCall.ID, Name: e.ToolCall.Name, Args: structMsg(e.ToolCall.Args), Partial: e.ToolCall.Partial}}
	case e.ToolResult != nil:
		out.Kind = &pb.TurnEvent_ToolResult{ToolResult: &pb.ToolResult{Id: e.ToolResult.ID, Name: e.ToolResult.Name, Result: structMsg(e.ToolResult.Result)}}
	}
	return out
}

func actionKind(k tools.ActionKind) pb.ActionKind {
	switch k {
	case tools.ActionCommand:
		return pb.ActionKind_ACTION_KIND_COMMAND
	case tools.ActionWrite:
		return pb.ActionKind_ACTION_KIND_WRITE
	case tools.ActionDelete:
		return pb.ActionKind_ACTION_KIND_DELETE
	case tools.ActionNetwork:
		return pb.ActionKind_ACTION_KIND_NETWORK
	case tools.ActionMCP:
		return pb.ActionKind_ACTION_KIND_MCP
	}
	return pb.ActionKind_ACTION_KIND_UNSPECIFIED
}

func decision(d pb.Decision) tools.Decision {
	switch d {
	case pb.Decision_DECISION_ONCE:
		return tools.DecisionOnce
	case pb.Decision_DECISION_SESSION:
		return tools.DecisionSession
	case pb.Decision_DECISION_ALWAYS:
		return tools.DecisionAlways
	}
	return tools.DecisionDeny
}
