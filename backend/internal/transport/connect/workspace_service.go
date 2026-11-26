package connectsvc

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/store"
)

const (
	maxWorkspaceChunk          = 1 << 20
	maxWorkspaceListPageSize   = 500
	maxWorkspaceListPageOffset = 1_000_000
)

type WorkspaceService struct{ registry *session.Registry }

func NewWorkspaceService(registry *session.Registry) *WorkspaceService {
	return &WorkspaceService{registry: registry}
}

func workspacePath(name string) (string, error) {
	if name == "" {
		return ".", nil
	}
	name = filepath.FromSlash(name)
	if !filepath.IsLocal(name) {
		return "", os.ErrNotExist
	}
	return name, nil
}

func (s *WorkspaceService) root(ctx context.Context, sessionID string) (*os.Root, error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	root, err := s.registry.OpenWorkspaceRoot(ctx, sessionID, userID)
	if err != nil {
		return nil, workspaceError(err)
	}
	return root, nil
}

func workspaceEntry(name, path string, info fs.FileInfo) (*v1.WorkspaceEntry, error) {
	mode := info.Mode()
	if !mode.IsRegular() && !mode.IsDir() && mode.Type()&os.ModeSymlink == 0 {
		return nil, fs.ErrPermission
	}
	return &v1.WorkspaceEntry{
		Name: name, Path: filepath.ToSlash(path), Mode: uint32(mode.Perm()), Size: info.Size(),
		ModifiedAt: info.ModTime().UnixMilli(), Directory: mode.IsDir(), Symlink: mode.Type()&os.ModeSymlink != 0,
	}, nil
}

func (s *WorkspaceService) Stat(ctx context.Context, req *connect.Request[v1.WorkspacePath]) (*connect.Response[v1.WorkspaceStatResponse], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, workspaceError(err)
	}
	entry, err := workspaceEntry(info.Name(), name, info)
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.WorkspaceStatResponse{Entry: entry}), nil
}

func (s *WorkspaceService) List(ctx context.Context, req *connect.Request[v1.WorkspaceListRequest]) (*connect.Response[v1.WorkspaceListResponse], error) {
	pageSize, offset, paginated, err := workspaceListPage(req.Msg.PageSize, req.Msg.PageToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	dir, err := root.Open(name)
	if err != nil {
		return nil, workspaceError(err)
	}
	defer dir.Close()
	response, err := listWorkspaceDirectory(ctx, dir, name, pageSize, offset, paginated)
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(response), nil
}

func listWorkspaceDirectory(ctx context.Context, dir *os.File, name string, pageSize, offset int, paginated bool) (*v1.WorkspaceListResponse, error) {
	if paginated {
		for remaining := offset; remaining > 0; {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			skipped, err := dir.ReadDir(min(remaining, maxWorkspaceListPageSize))
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			remaining -= len(skipped)
			if remaining > 0 && (errors.Is(err, io.EOF) || len(skipped) == 0) {
				return &v1.WorkspaceListResponse{}, nil
			}
		}
		entries, err := dir.ReadDir(pageSize + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		hasNextPage := len(entries) > pageSize
		if hasNextPage {
			entries = entries[:pageSize]
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		response := &v1.WorkspaceListResponse{Entries: make([]*v1.WorkspaceEntry, 0, len(entries))}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			item, err := workspaceEntry(entry.Name(), filepath.Join(name, entry.Name()), info)
			if errors.Is(err, fs.ErrPermission) {
				continue
			}
			if err != nil {
				return nil, err
			}
			response.Entries = append(response.Entries, item)
		}
		if hasNextPage && offset+len(entries) < maxWorkspaceListPageOffset {
			response.NextPageToken = strconv.Itoa(offset + len(entries))
		}
		return response, nil
	}
	entries := make([]fs.DirEntry, 0, maxWorkspaceListPageSize)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := dir.ReadDir(maxWorkspaceListPageSize)
		entries = append(entries, chunk...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	response := &v1.WorkspaceListResponse{Entries: make([]*v1.WorkspaceEntry, 0, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		item, err := workspaceEntry(entry.Name(), filepath.Join(name, entry.Name()), info)
		if errors.Is(err, fs.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, err
		}
		response.Entries = append(response.Entries, item)
	}
	return response, nil
}

func workspaceListPage(pageSize int32, token string) (int, int, bool, error) {
	if pageSize == 0 {
		if token != "" {
			return 0, 0, false, errors.New("page_token requires page_size")
		}
		return 0, 0, false, nil
	}
	if pageSize < 1 || pageSize > maxWorkspaceListPageSize {
		return 0, 0, false, errors.New("invalid page_size")
	}
	if token == "" {
		return int(pageSize), 0, true, nil
	}
	for _, char := range token {
		if char < '0' || char > '9' {
			return 0, 0, false, errors.New("invalid page_token")
		}
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset > maxWorkspaceListPageOffset {
		return 0, 0, false, errors.New("invalid page_token")
	}
	return int(pageSize), offset, true, nil
}

func (s *WorkspaceService) Read(ctx context.Context, req *connect.Request[v1.WorkspaceReadRequest]) (*connect.Response[v1.WorkspaceReadResponse], error) {
	if req.Msg.Offset < 0 || req.Msg.Size < 0 || req.Msg.Size > maxWorkspaceChunk {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid read range"))
	}
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, workspaceError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fs.ErrPermission
		}
		return nil, workspaceError(err)
	}
	content := make([]byte, req.Msg.Size)
	n, err := file.ReadAt(content, req.Msg.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.WorkspaceReadResponse{Content: content[:n]}), nil
}

func (s *WorkspaceService) Write(ctx context.Context, req *connect.Request[v1.WorkspaceWriteRequest]) (*connect.Response[v1.WorkspaceWriteResponse], error) {
	if req.Msg.Offset < 0 || len(req.Msg.Content) > maxWorkspaceChunk {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid write range"))
	}
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	if info, statErr := root.Lstat(name); statErr == nil && !info.Mode().IsRegular() && info.Mode().Type()&os.ModeSymlink == 0 {
		return nil, workspaceError(fs.ErrPermission)
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, workspaceError(err)
	}
	defer file.Close()
	n, err := file.WriteAt(req.Msg.Content, req.Msg.Offset)
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.WorkspaceWriteResponse{Written: int32(n)}), nil
}

func (s *WorkspaceService) Mkdir(ctx context.Context, req *connect.Request[v1.WorkspaceMkdirRequest]) (*connect.Response[v1.Empty], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	mode := os.FileMode(req.Msg.Mode)
	if mode == 0 {
		mode = 0o755
	}
	if err := root.Mkdir(name, mode.Perm()); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Remove(ctx context.Context, req *connect.Request[v1.WorkspaceRemoveRequest]) (*connect.Response[v1.Empty], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	if name == "." {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cannot remove workspace root"))
	}
	if err := root.Remove(name); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Rename(ctx context.Context, req *connect.Request[v1.WorkspaceRenameRequest]) (*connect.Response[v1.Empty], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	oldName, err := workspacePath(req.Msg.OldPath)
	if err != nil {
		return nil, workspaceError(err)
	}
	newName, err := workspacePath(req.Msg.NewPath)
	if err != nil {
		return nil, workspaceError(err)
	}
	if oldName == "." || newName == "." {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cannot rename workspace root"))
	}
	if err := root.Rename(oldName, newName); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Truncate(ctx context.Context, req *connect.Request[v1.WorkspaceTruncateRequest]) (*connect.Response[v1.Empty], error) {
	if req.Msg.Size < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid size"))
	}
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	file, err := root.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return nil, workspaceError(err)
	}
	defer file.Close()
	if err := file.Truncate(req.Msg.Size); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Chmod(ctx context.Context, req *connect.Request[v1.WorkspaceChmodRequest]) (*connect.Response[v1.Empty], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	if err := root.Chmod(name, os.FileMode(req.Msg.Mode).Perm()); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Chtimes(ctx context.Context, req *connect.Request[v1.WorkspaceChtimesRequest]) (*connect.Response[v1.Empty], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	if err := root.Chtimes(name, time.UnixMilli(req.Msg.AccessedAt), time.UnixMilli(req.Msg.ModifiedAt)); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Symlink(ctx context.Context, req *connect.Request[v1.WorkspaceSymlinkRequest]) (*connect.Response[v1.Empty], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	if req.Msg.Target == "" || filepath.IsAbs(req.Msg.Target) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid symlink target"))
	}
	if err := root.Symlink(filepath.FromSlash(req.Msg.Target), name); err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *WorkspaceService) Readlink(ctx context.Context, req *connect.Request[v1.WorkspacePath]) (*connect.Response[v1.WorkspaceReadlinkResponse], error) {
	root, err := s.root(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := workspacePath(req.Msg.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	target, err := root.Readlink(name)
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&v1.WorkspaceReadlinkResponse{Target: filepath.ToSlash(target)}), nil
}

func workspaceError(err error) error {
	if connectErr, ok := err.(*connect.Error); ok {
		return connectErr
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, fs.ErrExist):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, fs.ErrPermission):
		return connect.NewError(connect.CodePermissionDenied, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
