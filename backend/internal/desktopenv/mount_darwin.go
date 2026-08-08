//go:build darwin && cgo

package desktopenv

import (
	"context"
	"errors"
	"hash/fnv"
	"os"
	"path"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/anacrolix/fuse"
	fusefs "github.com/anacrolix/fuse/fs"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

type fuseMount struct {
	path string
	conn *fuse.Conn
}

func (m *fuseMount) Close() error {
	err := fuse.Unmount(m.path)
	_ = m.conn.Close()
	return err
}

type remoteFS struct {
	manager       *Manager
	environmentID string
	sessionID     string
}

type remoteNode struct {
	fs   *remoteFS
	path string
}

func (m *Manager) platformMount(ctx context.Context, environmentID string, mount Mount) (mountHandle, error) {
	if _, err := os.Stat("/usr/local/bin/go-nfsv4"); err != nil {
		return nil, errors.New("FUSE-T не установлен: brew install macos-fuse-t/homebrew-cask/fuse-t")
	}
	if err := os.MkdirAll(mount.Path, 0o755); err != nil {
		return nil, err
	}
	connection, err := fuse.Mount(mount.Path, fuse.FSName("Brigade"), fuse.VolumeName("Brigade"), fuse.NoAppleDouble(), fuse.NoAppleXattr())
	if err != nil {
		return nil, err
	}
	filesystem := &remoteFS{manager: m, environmentID: environmentID, sessionID: mount.SessionID}
	go func() {
		if err := fusefs.Serve(connection, filesystem); err != nil {
			m.mu.Lock()
			m.resourceErrors[mount.ID] = err.Error()
			m.mu.Unlock()
		}
	}()
	return &fuseMount{path: mount.Path, conn: connection}, nil
}

func (f *remoteFS) Root() (fusefs.Node, error) { return &remoteNode{fs: f, path: ""}, nil }

func (f *remoteFS) client(ctx context.Context) (brigadev1connect.WorkspaceServiceClient, string, error) {
	environment, token, err := f.manager.tokenFor(ctx, f.environmentID)
	if err != nil {
		return nil, "", err
	}
	return brigadev1connect.NewWorkspaceServiceClient(f.manager.http, environment.BaseURL), token, nil
}

func authorized[T any](token string, message T) *connect.Request[T] {
	request := connect.NewRequest(&message)
	request.Header().Set("Authorization", "Bearer "+token)
	return request
}

func (n *remoteNode) stat(ctx context.Context) (*v1.WorkspaceEntry, error) {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return nil, err
	}
	response, err := client.Stat(ctx, authorized(token, v1.WorkspacePath{SessionId: n.fs.sessionID, Path: n.path}))
	if err != nil {
		return nil, fuseError(err)
	}
	return response.Msg.Entry, nil
}

func (n *remoteNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	entry, err := n.stat(ctx)
	if err != nil {
		return err
	}
	attr.Valid = time.Second
	attr.Mode = os.FileMode(entry.Mode)
	if entry.Directory {
		attr.Mode |= os.ModeDir
	}
	if entry.Symlink {
		attr.Mode |= os.ModeSymlink
	}
	attr.Size = uint64(entry.Size)
	attr.Mtime = time.UnixMilli(entry.ModifiedAt)
	attr.Atime, attr.Ctime = attr.Mtime, attr.Mtime
	attr.Nlink = 1
	attr.Inode = inode(n.path)
	return nil
}

func inode(value string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return hash.Sum64() + 2
}

func (n *remoteNode) Lookup(ctx context.Context, name string) (fusefs.Node, error) {
	child := &remoteNode{fs: n.fs, path: path.Join(n.path, name)}
	_, err := child.stat(ctx)
	return child, err
}

func (n *remoteNode) Open(_ context.Context, _ *fuse.OpenRequest, response *fuse.OpenResponse) (fusefs.Handle, error) {
	response.Flags |= fuse.OpenDirectIO
	return n, nil
}

func (n *remoteNode) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return nil, err
	}
	response, err := client.List(ctx, authorized(token, v1.WorkspacePath{SessionId: n.fs.sessionID, Path: n.path}))
	if err != nil {
		return nil, fuseError(err)
	}
	entries := make([]fuse.Dirent, 0, len(response.Msg.Entries))
	for _, entry := range response.Msg.Entries {
		typeID := fuse.DT_File
		if entry.Directory {
			typeID = fuse.DT_Dir
		}
		if entry.Symlink {
			typeID = fuse.DT_Link
		}
		entries = append(entries, fuse.Dirent{Inode: inode(entry.Path), Name: entry.Name, Type: typeID})
	}
	return entries, nil
}

func (n *remoteNode) Read(ctx context.Context, request *fuse.ReadRequest, response *fuse.ReadResponse) error {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return err
	}
	result, err := client.Read(ctx, authorized(token, v1.WorkspaceReadRequest{SessionId: n.fs.sessionID, Path: n.path, Offset: request.Offset, Size: int32(request.Size)}))
	if err != nil {
		return fuseError(err)
	}
	response.Data = result.Msg.Content
	return nil
}

func (n *remoteNode) Write(ctx context.Context, request *fuse.WriteRequest, response *fuse.WriteResponse) error {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return err
	}
	result, err := client.Write(ctx, authorized(token, v1.WorkspaceWriteRequest{SessionId: n.fs.sessionID, Path: n.path, Offset: request.Offset, Content: request.Data}))
	if err != nil {
		return fuseError(err)
	}
	response.Size = int(result.Msg.Written)
	return nil
}

func (n *remoteNode) Create(ctx context.Context, request *fuse.CreateRequest, _ *fuse.CreateResponse) (fusefs.Node, fusefs.Handle, error) {
	child := &remoteNode{fs: n.fs, path: path.Join(n.path, request.Name)}
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err = client.Write(ctx, authorized(token, v1.WorkspaceWriteRequest{SessionId: n.fs.sessionID, Path: child.path})); err != nil {
		return nil, nil, fuseError(err)
	}
	_, _ = client.Chmod(ctx, authorized(token, v1.WorkspaceChmodRequest{SessionId: n.fs.sessionID, Path: child.path, Mode: uint32(request.Mode.Perm())}))
	return child, child, nil
}

func (n *remoteNode) Mkdir(ctx context.Context, request *fuse.MkdirRequest) (fusefs.Node, error) {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return nil, err
	}
	child := &remoteNode{fs: n.fs, path: path.Join(n.path, request.Name)}
	_, err = client.Mkdir(ctx, authorized(token, v1.WorkspaceMkdirRequest{SessionId: n.fs.sessionID, Path: child.path, Mode: uint32(request.Mode.Perm())}))
	if err != nil {
		return nil, fuseError(err)
	}
	return child, nil
}

func (n *remoteNode) Remove(ctx context.Context, request *fuse.RemoveRequest) error {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return err
	}
	_, err = client.Remove(ctx, authorized(token, v1.WorkspaceRemoveRequest{SessionId: n.fs.sessionID, Path: path.Join(n.path, request.Name), Directory: request.Dir}))
	return fuseError(err)
}

func (n *remoteNode) Rename(ctx context.Context, request *fuse.RenameRequest, newDirectory fusefs.Node) error {
	target, ok := newDirectory.(*remoteNode)
	if !ok {
		return fuse.EIO
	}
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return err
	}
	_, err = client.Rename(ctx, authorized(token, v1.WorkspaceRenameRequest{SessionId: n.fs.sessionID, OldPath: path.Join(n.path, request.OldName), NewPath: path.Join(target.path, request.NewName)}))
	return fuseError(err)
}

func (n *remoteNode) Setattr(ctx context.Context, request *fuse.SetattrRequest, _ *fuse.SetattrResponse) error {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return err
	}
	if request.Valid.Size() {
		if _, err = client.Truncate(ctx, authorized(token, v1.WorkspaceTruncateRequest{SessionId: n.fs.sessionID, Path: n.path, Size: int64(request.Size)})); err != nil {
			return fuseError(err)
		}
	}
	if request.Valid.Mode() {
		if _, err = client.Chmod(ctx, authorized(token, v1.WorkspaceChmodRequest{SessionId: n.fs.sessionID, Path: n.path, Mode: uint32(request.Mode.Perm())})); err != nil {
			return fuseError(err)
		}
	}
	if request.Valid.Atime() || request.Valid.Mtime() || request.Valid.AtimeNow() || request.Valid.MtimeNow() {
		now := time.Now()
		atime, mtime := request.Atime, request.Mtime
		if !request.Valid.Atime() {
			atime = now
		}
		if !request.Valid.Mtime() {
			mtime = now
		}
		_, err = client.Chtimes(ctx, authorized(token, v1.WorkspaceChtimesRequest{SessionId: n.fs.sessionID, Path: n.path, AccessedAt: atime.UnixMilli(), ModifiedAt: mtime.UnixMilli()}))
		if err != nil {
			return fuseError(err)
		}
	}
	return nil
}

func (n *remoteNode) Symlink(ctx context.Context, request *fuse.SymlinkRequest) (fusefs.Node, error) {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return nil, err
	}
	child := &remoteNode{fs: n.fs, path: path.Join(n.path, request.NewName)}
	_, err = client.Symlink(ctx, authorized(token, v1.WorkspaceSymlinkRequest{SessionId: n.fs.sessionID, Target: request.Target, Path: child.path}))
	if err != nil {
		return nil, fuseError(err)
	}
	return child, nil
}

func (n *remoteNode) Readlink(ctx context.Context, _ *fuse.ReadlinkRequest) (string, error) {
	client, token, err := n.fs.client(ctx)
	if err != nil {
		return "", err
	}
	response, err := client.Readlink(ctx, authorized(token, v1.WorkspacePath{SessionId: n.fs.sessionID, Path: n.path}))
	if err != nil {
		return "", fuseError(err)
	}
	return response.Msg.Target, nil
}

func fuseError(err error) error {
	if err == nil {
		return nil
	}
	var connectError *connect.Error
	if errors.As(err, &connectError) {
		switch connectError.Code() {
		case connect.CodeNotFound:
			return fuse.ENOENT
		case connect.CodeAlreadyExists:
			return fuse.EEXIST
		case connect.CodePermissionDenied, connect.CodeUnauthenticated:
			return fuse.EPERM
		case connect.CodeInvalidArgument:
			return syscall.EINVAL
		}
	}
	return syscall.EIO
}
