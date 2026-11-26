// SPDX-License-Identifier: MPL-2.0

package nodestorage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/athNdev/carbon-panel/pkg/files"
)

// AllocationRoot provides exclusive allocation immediately beneath the node's
// configured server storage directory. It is used only for new server creation.
type AllocationRoot interface {
	Accessor
	MkdirExclusive(child string) error
	FreeBytes() (int64, error)
}

// ForAllocation opens the storage parent for an independently allocated server.
func (r *Resolver) ForAllocation(ctx context.Context, node NodeInfo, localParent string) (AllocationRoot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !node.IsLocal && RequiresRemoteTransport(node) {
		type storageRootTransport interface {
			OpenStorageRoot(context.Context, NodeInfo) (AllocationRoot, error)
		}
		if r == nil {
			return nil, ErrUnavailable
		}
		transport, ok := r.transport.(storageRootTransport)
		if !ok {
			return nil, ErrUnavailable
		}
		return transport.OpenStorageRoot(ctx, node)
	}
	root := localParent
	if !node.IsLocal {
		root = sharedStorageRoot(node)
	}
	if root == "" {
		return nil, ErrUnavailable
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// The allocation root already exists. Pin it directly so rollback cannot
	// follow a replacement at this path while the allocation is held open.
	parentPath := filepath.Dir(abs)
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, err
	}
	if parentPath == abs {
		return &local{root: parent, dir: "."}, nil
	}
	pinned, err := parent.OpenRoot(filepath.Base(abs))
	_ = parent.Close()
	if err != nil {
		return nil, err
	}
	return &local{root: pinned, dir: "."}, nil
}

func (l *local) MkdirExclusive(child string) error {
	if child == "." || child == ".." || filepath.Base(child) != child {
		return fmt.Errorf("invalid server directory name")
	}
	p, err := l.sub(child)
	if err != nil {
		return err
	}
	return l.root.Mkdir(p, 0o755)
}

func (l *local) FreeBytes() (int64, error) {
	return files.GetFreeDiskSpace(l.Root())
}

// ForExisting opens an existing server tree without creating a missing path.
func (r *Resolver) ForExisting(ctx context.Context, node NodeInfo, dataRoot string) (Accessor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if RequiresRemoteTransport(node) {
		type existingRootTransport interface {
			OpenExisting(context.Context, NodeInfo, string) (Accessor, error)
		}
		if r == nil {
			return nil, ErrUnavailable
		}
		transport, ok := r.transport.(existingRootTransport)
		if !ok {
			return nil, ErrUnavailable
		}
		return transport.OpenExisting(ctx, node, dataRoot)
	}
	root, err := MapDataPath(node, dataRoot)
	if err != nil {
		return nil, err
	}
	acc, err := NewLocal(root)
	if err != nil {
		return nil, err
	}
	local := acc.(*local)
	info, err := local.root.Lstat(local.dir)
	if err != nil || !info.IsDir() {
		_ = local.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrUnavailable
	}
	return local, nil
}
