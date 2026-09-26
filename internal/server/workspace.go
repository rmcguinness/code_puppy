package server

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// workspaceService implements WorkspaceService.
type workspaceService struct{ s *Server }

type req[T any] = *connect.Request[T]

func ok[T any](msg *T) (*connect.Response[T], error) { return connect.NewResponse(msg), nil }

func (h workspaceService) ListWorkspaces(context.Context, req[pb.ListWorkspacesRequest]) (*connect.Response[pb.ListWorkspacesResponse], error) {
	return ok(&pb.ListWorkspacesResponse{Workspaces: h.s.openDirs()})
}

func (h workspaceService) CloseWorkspace(_ context.Context, r req[pb.CloseWorkspaceRequest]) (*connect.Response[pb.CloseWorkspaceResponse], error) {
	if err := h.s.closeWorkspace(r.Msg.Workspace); err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.CloseWorkspaceResponse{})
}

func (h workspaceService) GetSandbox(ctx context.Context, r req[pb.GetSandboxRequest]) (*connect.Response[pb.GetSandboxResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	return ok(&pb.GetSandboxResponse{Summary: w.SandboxSummary()})
}

func (h workspaceService) ListAgents(ctx context.Context, r req[pb.ListAgentsRequest]) (*connect.Response[pb.ListAgentsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	out := &pb.ListAgentsResponse{}
	for _, a := range w.ListAgents() {
		out.Agents = append(out.Agents, agentMsg(a))
	}
	return ok(out)
}

func (h workspaceService) SetAgent(ctx context.Context, r req[pb.SetAgentRequest]) (*connect.Response[pb.SetAgentResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	a, err := w.SetAgent(ctx, r.Msg.Name)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.SetAgentResponse{Agent: agentMsg(a)})
}

func (h workspaceService) GetModel(ctx context.Context, r req[pb.GetModelRequest]) (*connect.Response[pb.GetModelResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	m := w.Model()
	out := &pb.GetModelResponse{Name: m.Name, Provider: m.Provider}
	if err := w.ModelErr(); err != nil {
		out.Unavailable = app.ModelErrorSummary(err, w.Config())
	}
	return ok(out)
}

func (h workspaceService) SetModel(ctx context.Context, r req[pb.SetModelRequest]) (*connect.Response[pb.SetModelResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	pin, err := w.SetModel(ctx, r.Msg.Ref)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.SetModelResponse{ActivePin: pin})
}

func (h workspaceService) PinModel(ctx context.Context, r req[pb.PinModelRequest]) (*connect.Response[pb.PinModelResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	res, err := w.PinModel(ctx, r.Msg.Agent, r.Msg.Ref)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.PinModelResponse{Agent: res.Agent, Model: res.Model, Saved: savedMsg(res.Saved)})
}

func (h workspaceService) UnpinModel(ctx context.Context, r req[pb.UnpinModelRequest]) (*connect.Response[pb.UnpinModelResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	res, err := w.Unpin(ctx, r.Msg.Agent)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.UnpinModelResponse{Agent: res.Agent, Model: res.Model, Saved: savedMsg(res.Saved)})
}

func (h workspaceService) GetModelSettings(ctx context.Context, r req[pb.GetModelSettingsRequest]) (*connect.Response[pb.GetModelSettingsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	if r.Msg.Ref == "" {
		out := &pb.GetModelSettingsResponse{All: map[string]*pb.ModelSettings{}}
		for name, s := range w.AllModelSettings() {
			out.All[name] = modelSettingsMsg(s)
		}
		return ok(out)
	}
	info, err := w.ModelSettings(r.Msg.Ref)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.GetModelSettingsResponse{Model: modelSettingsInfoMsg(info)})
}

func (h workspaceService) UpdateModelSettings(ctx context.Context, r req[pb.UpdateModelSettingsRequest]) (*connect.Response[pb.UpdateModelSettingsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	var changes []app.Setting
	for _, c := range r.Msg.Changes {
		changes = append(changes, app.Setting{Key: c.Key, Value: c.Value})
	}
	res, err := w.UpdateModelSettings(r.Msg.Ref, r.Msg.Reset_, changes)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.UpdateModelSettingsResponse{Model: modelSettingsInfoMsg(res.ModelSettingsInfo), Unsupported: res.Unsupported, Saved: savedMsg(res.Saved)})
}

func (h workspaceService) GetSettings(ctx context.Context, r req[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	st := w.Settings()
	return ok(&pb.GetSettingsResponse{
		PuppyName: st.PuppyName, OwnerName: st.OwnerName, Agency: st.Agency,
		Model: st.Model.Name, Provider: st.Model.Provider, Agent: st.Agent, Locale: st.Locale,
		ImagesEnabled: w.ImagesEnabled(),
	})
}

func (h workspaceService) SetSetting(ctx context.Context, r req[pb.SetSettingRequest]) (*connect.Response[pb.SetSettingResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	key, err := w.Set(ctx, r.Msg.Key, r.Msg.Value)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.SetSettingResponse{Key: key})
}

func (h workspaceService) ListSkills(ctx context.Context, r req[pb.ListSkillsRequest]) (*connect.Response[pb.ListSkillsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	list := w.ListSkills()
	if r.Msg.Query != "" {
		list = w.SearchSkills(r.Msg.Query)
	}
	return ok(&pb.ListSkillsResponse{Skills: skillMsgs(list)})
}

func (h workspaceService) GetSkill(ctx context.Context, r req[pb.GetSkillRequest]) (*connect.Response[pb.GetSkillResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	s, found := w.Skill(r.Msg.Name)
	if !found {
		return nil, apiError(connect.CodeNotFound, "SKILL_NOT_FOUND", fmt.Errorf("no skill %q", r.Msg.Name), "name", r.Msg.Name)
	}
	return ok(&pb.GetSkillResponse{Skill: skillMsg(s)})
}

func (h workspaceService) ListEnvs(ctx context.Context, r req[pb.ListEnvsRequest]) (*connect.Response[pb.ListEnvsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	list, err := w.ListEnvs()
	if err != nil {
		return nil, toAPI(err)
	}
	out := &pb.ListEnvsResponse{}
	for _, e := range list {
		out.Envs = append(out.Envs, &pb.Env{Key: e.Key, Deps: e.Deps, Skills: e.Skills, Size: e.Size, LastUsed: timestamp(e.LastUsed), Ready: e.Ready})
	}
	return ok(out)
}

func (h workspaceService) RemoveEnv(ctx context.Context, r req[pb.RemoveEnvRequest]) (*connect.Response[pb.RemoveEnvResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	if err := w.RemoveEnv(r.Msg.Key); err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.RemoveEnvResponse{})
}

func (h workspaceService) PruneEnvs(ctx context.Context, r req[pb.PruneEnvsRequest]) (*connect.Response[pb.PruneEnvsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	res, err := w.PruneEnvs()
	if err != nil {
		return nil, toAPI(err)
	}
	out := &pb.PruneEnvsResponse{Removed: int32(res.Removed), Freed: res.Freed}
	for _, f := range res.Failed {
		out.Failed = append(out.Failed, &pb.EnvError{Key: f.Key, Error: f.Err.Error()})
	}
	return ok(out)
}

func (h workspaceService) ListMCPServers(ctx context.Context, r req[pb.ListMCPServersRequest]) (*connect.Response[pb.ListMCPServersResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	out := &pb.ListMCPServersResponse{}
	for _, m := range w.ListMCPServers() {
		out.Servers = append(out.Servers, &pb.MCPServer{Name: m.Name, Target: m.Target, AutoApprove: m.AutoApprove})
	}
	return ok(out)
}

func (h workspaceService) ListTools(ctx context.Context, r req[pb.ListToolsRequest]) (*connect.Response[pb.ListToolsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	at := w.ActiveAgentTools()
	out := &pb.ListToolsResponse{Agent: at.Agent}
	for _, t := range at.Tools {
		out.Tools = append(out.Tools, &pb.ToolInfo{Name: t.Name, Description: t.Description, PlanAllowed: t.PlanAllowed})
	}
	for _, m := range at.MCP {
		out.Mcp = append(out.Mcp, &pb.MCPOffer{Server: m.Server, Tools: m.Tools, Prefix: m.Prefix})
	}
	return ok(out)
}

func (h workspaceService) ReloadMemory(ctx context.Context, r req[pb.ReloadMemoryRequest]) (*connect.Response[pb.ReloadMemoryResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	paths, err := w.ReloadMemory(ctx)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.ReloadMemoryResponse{Paths: paths, Files: w.MemoryFiles()})
}

func (h workspaceService) AddMemory(ctx context.Context, r req[pb.AddMemoryRequest]) (*connect.Response[pb.AddMemoryResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	p, err := w.AddMemory(ctx, r.Msg.Text)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.AddMemoryResponse{Path: p})
}

func (h workspaceService) ListLocales(ctx context.Context, r req[pb.ListLocalesRequest]) (*connect.Response[pb.ListLocalesResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	list, dir := w.AvailableLocales()
	out := &pb.ListLocalesResponse{CustomDir: dir}
	for _, l := range list {
		out.Locales = append(out.Locales, &pb.LocaleInfo{Tag: l.Tag, Name: l.Name})
	}
	return ok(out)
}

func (h workspaceService) SetLocale(ctx context.Context, r req[pb.SetLocaleRequest]) (*connect.Response[pb.SetLocaleResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	res, err := w.SetLocale(ctx, r.Msg.Input)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.SetLocaleResponse{Tag: res.Tag, NativeName: res.NativeName, LanguageName: res.LanguageName, HasCatalog: res.HasCatalog, Saved: savedMsg(res.Saved)})
}

func (h workspaceService) ListCheckpoints(ctx context.Context, r req[pb.ListCheckpointsRequest]) (*connect.Response[pb.ListCheckpointsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	out := &pb.ListCheckpointsResponse{}
	for _, c := range w.ListCheckpoints() {
		out.Checkpoints = append(out.Checkpoints, &pb.Checkpoint{Id: int32(c.ID), Label: c.Label, Time: timestamppb.New(c.Time), Files: c.Files})
	}
	return ok(out)
}

func (h workspaceService) Undo(ctx context.Context, r req[pb.UndoRequest]) (*connect.Response[pb.UndoResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	res, err := w.Undo(r.Msg.Force)
	// A conflict before anything was restored fails the call; anything
	// else is reported with what was restored.
	if err != nil && len(res.Restored) == 0 && errors.Is(err, app.ErrUndoConflict) {
		return nil, toAPI(err)
	}
	return ok(&pb.UndoResponse{Label: res.Label, Restored: res.Restored, Error: errorInfo(err)})
}

func (h workspaceService) GetDiff(ctx context.Context, r req[pb.GetDiffRequest]) (*connect.Response[pb.GetDiffResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	if !r.Msg.Git {
		return ok(&pb.GetDiffResponse{Diff: w.SessionDiff()})
	}
	out, err := w.GitDiff(ctx, r.Msg.Color)
	if err != nil {
		return nil, apiError(connect.CodeFailedPrecondition, "GIT_FAILED", fmt.Errorf("%w: %s", err, out))
	}
	return ok(&pb.GetDiffResponse{Diff: out})
}

func (h workspaceService) ListApprovals(ctx context.Context, r req[pb.ListApprovalsRequest]) (*connect.Response[pb.ListApprovalsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	out := &pb.ListApprovalsResponse{}
	for _, a := range w.ListApprovals() {
		out.Approvals = append(out.Approvals, approvalMsg(a))
	}
	return ok(out)
}

func (h workspaceService) RevokeApprovals(ctx context.Context, r req[pb.RevokeApprovalsRequest]) (*connect.Response[pb.RevokeApprovalsResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	n := 0
	if r.Msg.All {
		n = w.ClearApprovals()
	} else {
		n = w.RevokeApprovals(r.Msg.Keys...)
	}
	return ok(&pb.RevokeApprovalsResponse{Revoked: int32(n)})
}

func (h workspaceService) LoadImage(ctx context.Context, r req[pb.LoadImageRequest]) (*connect.Response[pb.LoadImageResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	img, err := w.LoadImage(r.Msg.Path)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.LoadImageResponse{Image: w.keepImage(img)})
}

func (h workspaceService) AddImage(ctx context.Context, r req[pb.AddImageRequest]) (*connect.Response[pb.AddImageResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	img, err := w.AddImage(r.Msg.Name, r.Msg.Data)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.AddImageResponse{Image: w.keepImage(img)})
}

func (h workspaceService) GetSearchProvider(ctx context.Context, r req[pb.GetSearchProviderRequest]) (*connect.Response[pb.GetSearchProviderResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	provider, err := w.SearchProvider()
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.GetSearchProviderResponse{Provider: provider})
}

func (h workspaceService) SearchWeb(ctx context.Context, r req[pb.SearchWebRequest]) (*connect.Response[pb.SearchWebResponse], error) {
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	provider, err := w.SearchProvider()
	if err != nil {
		return nil, toAPI(err)
	}
	res, err := w.SearchWeb(ctx, r.Msg.Terms)
	if err != nil {
		return nil, toAPI(err)
	}
	out := &pb.SearchWebResponse{Provider: provider, Prompt: res.Prompt}
	for _, l := range res.Links {
		out.Links = append(out.Links, &pb.Link{Title: l.Title, Url: l.URL})
	}
	return ok(out)
}
