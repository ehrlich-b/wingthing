package localmcp

import (
	"encoding/json"
	"os"

	"github.com/ehrlich-b/wingthing/internal/worktree"
)

func (s *Server) worktreeManager(repo string) (worktree.Manager, string, error) {
	repo, err := s.resolveWorkingDirectory(repo)
	if err != nil {
		return worktree.Manager{}, "", err
	}
	paths := s.allowedPaths
	if len(paths) == 0 && !s.enforcePathBounds {
		// Unrestricted local MCP has the current OS user's full path authority.
		paths = []string{"/"}
	}
	return worktree.Manager{AllowedPaths: paths, Root: os.Getenv("WINGTHING_WORKTREE_ROOT")}, repo, nil
}

func (s *Server) toolWorktreeCreate(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Repo string `json:"repo"`
		Name string `json:"name"`
		Base string `json:"base"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	manager, repo, err := s.worktreeManager(args.Repo)
	if err != nil {
		return nil, err
	}
	var created *worktree.Worktree
	if err := s.admitSpawn(func() error {
		var err error
		created, err = manager.Create(repo, args.Name, args.Base)
		return err
	}); err != nil {
		return nil, err
	}
	return map[string]any{
		"name": created.Name, "repo": created.Repo, "path": created.Path,
		"cwd": created.Path, "branch": created.Branch, "head": created.Head,
		"checkout_required": created.CheckoutRequired,
	}, nil
}

func (s *Server) toolWorktreeList(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Repo string `json:"repo"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	manager, repo, err := s.worktreeManager(args.Repo)
	if err != nil {
		return nil, err
	}
	entries, err := manager.List(repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"worktrees": entries}, nil
}

func (s *Server) toolWorktreeRemove(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Repo  string `json:"repo"`
		Name  string `json:"name"`
		Force bool   `json:"force"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	manager, repo, err := s.worktreeManager(args.Repo)
	if err != nil {
		return nil, err
	}
	if err := manager.Remove(repo, args.Name, args.Force); err != nil {
		return nil, err
	}
	return map[string]any{"name": args.Name, "removed": true}, nil
}
