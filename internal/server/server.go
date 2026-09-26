// Package server serves Code Puppy's API (api/codepuppy/v1) over Connect:
// one process holding every workspace a user opens, each an app.Workspace.
// Handlers translate between the protos and internal/app; they hold no
// logic of their own.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sync"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1/codepuppyv1connect"
	"github.com/retail-cortex/code_puppy/internal/images"
)

// Opener opens the workspace in dir, an absolute directory. The server
// calls it once per workspace; each call must build its own configuration,
// since a workspace owns and changes it.
type Opener func(ctx context.Context, dir string) (*app.Workspace, error)

// Server holds the open workspaces and serves the API.
type Server struct {
	codepuppyv1connect.UnimplementedWorkerServiceHandler

	open   Opener
	broker *broker

	mu         sync.Mutex
	workspaces map[string]*workspace // by canonical directory
}

// workspace is an open workspace and what the server keeps for it.
type workspace struct {
	*app.Workspace

	mu     sync.Mutex
	images map[string]*images.Image // for Turn.image_ids, by ID
}

// New returns a server that opens workspaces with open.
func New(open Opener) *Server {
	return &Server{open: open, broker: newBroker(), workspaces: map[string]*workspace{}}
}

// Handler serves every service.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(codepuppyv1connect.NewSessionServiceHandler(sessionService{s}))
	mux.Handle(codepuppyv1connect.NewWorkspaceServiceHandler(workspaceService{s}))
	mux.Handle(codepuppyv1connect.NewWorkerServiceHandler(s))
	return mux
}

// Close closes every workspace.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for dir, w := range s.workspaces {
		errs = append(errs, w.Close())
		delete(s.workspaces, dir)
	}
	return errors.Join(errs...)
}

// canonical is the key a workspace is kept under: its absolute directory
// with symlinks resolved, so two spellings share one workspace.
func canonical(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("workspace %q is not an absolute directory", dir)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	return real, nil
}

// workspace returns the open workspace for dir, opening it on first use.
func (s *Server) workspace(ctx context.Context, dir string) (*workspace, error) {
	key, err := canonical(dir)
	if err != nil {
		return nil, apiError(connect.CodeInvalidArgument, "INVALID_WORKSPACE", err, "workspace", dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.workspaces[key]; ok {
		return w, nil
	}
	aw, err := s.open(ctx, key)
	if err != nil {
		return nil, apiError(connect.CodeFailedPrecondition, "OPEN_FAILED", err, "workspace", key)
	}
	// Approvals and questions go to the client running the turn.
	aw.Tools().Hooks().SetApprover(s.broker.approve)
	aw.Tools().Hooks().SetUserPrompter(s.broker.question)
	w := &workspace{Workspace: aw, images: map[string]*images.Image{}}
	s.workspaces[key] = w
	return w, nil
}

// closeWorkspace closes and forgets the workspace for dir, if open.
func (s *Server) closeWorkspace(dir string) error {
	key, err := canonical(dir)
	if err != nil {
		return apiError(connect.CodeInvalidArgument, "INVALID_WORKSPACE", err, "workspace", dir)
	}
	s.mu.Lock()
	w, ok := s.workspaces[key]
	delete(s.workspaces, key)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return w.Close()
}

// openDirs returns the open workspaces' directories, sorted.
func (s *Server) openDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for dir := range s.workspaces {
		out = append(out, dir)
	}
	slices.Sort(out)
	return out
}

// keepImage remembers an image for later turns and returns its message.
func (w *workspace) keepImage(img *images.Image) *pb.Image {
	w.mu.Lock()
	w.images[img.SHA256] = img
	w.mu.Unlock()
	return imageMsg(img)
}

// image returns a kept image by ID.
func (w *workspace) image(id string) (*images.Image, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	img, ok := w.images[id]
	if !ok {
		return nil, apiError(connect.CodeNotFound, "UNKNOWN_IMAGE", fmt.Errorf("no image %q: load or add it first", id), "id", id)
	}
	return img, nil
}

// apiError is a Connect error with an ErrorInfo detail. kv are metadata
// key-value pairs.
func apiError(code connect.Code, reason string, err error, kv ...string) *connect.Error {
	info := &pb.ErrorInfo{Reason: reason, Message: err.Error()}
	if len(kv) > 0 {
		info.Metadata = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			info.Metadata[kv[i]] = kv[i+1]
		}
	}
	ce := connect.NewError(code, err)
	if d, derr := connect.NewErrorDetail(info); derr == nil {
		ce.AddDetail(d)
	}
	return ce
}

// toAPI maps internal/app's typed errors to Connect codes and reasons.
func toAPI(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	var (
		unknownAgent   *app.UnknownAgentError
		resume         *app.ResumeError
		invalidSetting *app.InvalidSettingError
		unknownSetting *app.UnknownSettingError
		blocked        *app.BlockedError
	)
	switch {
	case errors.As(err, &unknownAgent):
		return apiError(connect.CodeNotFound, "UNKNOWN_AGENT", err, "name", unknownAgent.Name)
	case errors.As(err, &resume):
		return apiError(connect.CodeNotFound, "RESUME_FAILED", err)
	case errors.As(err, &invalidSetting):
		return apiError(connect.CodeInvalidArgument, "INVALID_SETTING", err)
	case errors.As(err, &unknownSetting):
		return apiError(connect.CodeInvalidArgument, "UNKNOWN_SETTING", err, "key", unknownSetting.Key)
	case errors.As(err, &blocked):
		return apiError(connect.CodePermissionDenied, "PROMPT_BLOCKED", err, "reason", blocked.Reason)
	}
	for _, m := range []struct {
		target error
		code   connect.Code
		reason string
	}{
		{app.ErrNoActiveSession, connect.CodeFailedPrecondition, "NO_ACTIVE_SESSION"},
		{app.ErrSnapshotNameTaken, connect.CodeAlreadyExists, "SNAPSHOT_NAME_TAKEN"},
		{app.ErrBadModelRef, connect.CodeInvalidArgument, "BAD_MODEL_REF"},
		{app.ErrInvalidAgency, connect.CodeInvalidArgument, "INVALID_AGENCY"},
		{app.ErrUndoConflict, connect.CodeFailedPrecondition, "UNDO_CONFLICT"},
		{app.ErrScriptsDisabled, connect.CodeFailedPrecondition, "SCRIPTS_DISABLED"},
		{app.ErrUnknownLocale, connect.CodeInvalidArgument, "UNKNOWN_LOCALE"},
		{app.ErrImagesDisabled, connect.CodeFailedPrecondition, "IMAGES_DISABLED"},
		{app.ErrNoFetch, connect.CodeFailedPrecondition, "NO_FETCH"},
		{app.ErrNoSearch, connect.CodeFailedPrecondition, "NO_SEARCH"},
		{app.ErrNothingToCompact, connect.CodeFailedPrecondition, "NOTHING_TO_COMPACT"},
	} {
		if errors.Is(err, m.target) {
			return apiError(m.code, m.reason, err)
		}
	}
	if errors.Is(err, context.Canceled) {
		return connect.NewError(connect.CodeCanceled, err)
	}
	return apiError(connect.CodeInternal, "INTERNAL", err)
}

// errorInfo is err as an ErrorInfo message, for errors reported inside a
// response rather than as the call's error.
func errorInfo(err error) *pb.ErrorInfo {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(toAPI(err), &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if info, ok := v.(*pb.ErrorInfo); ok {
					return info
				}
			}
		}
	}
	return &pb.ErrorInfo{Reason: "INTERNAL", Message: err.Error()}
}
