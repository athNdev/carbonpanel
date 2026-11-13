// SPDX-License-Identifier: MPL-2.0
package nodestorage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/athNdev/carbon-panel/internal/storageworker"
)

// WriteFrom copies at most limit bytes into an owned transaction and promotes
// only after the source reaches EOF successfully. The source belongs to caller.
func WriteFrom(ctx context.Context, acc Accessor, rel string, src io.Reader, limit int64) (int64, error) {
	w, err := acc.BeginWrite(ctx, rel, limit)
	if err != nil {
		return 0, err
	}
	defer w.Close()
	n, err := io.CopyBuffer(w, io.LimitReader(src, limit+1), make([]byte, storageworker.ChunkBytes))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("storage: stream exceeds %d byte limit", limit)
	}
	if err := w.Commit(); err != nil {
		return n, err
	}
	return n, nil
}

// IsWriteStaging identifies reserved transaction files, which are recovery
// artifacts rather than world content and must be excluded from archives.
func IsWriteStaging(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".carbon-panel/undo" || strings.HasPrefix(clean, ".carbon-panel/undo/") {
		return true
	}
	name := filepath.Base(rel)
	return strings.HasPrefix(name, ".carbon-write-") && strings.HasSuffix(name, ".partial")
}

func (l *local) OpenRead(ctx context.Context, rel string) (storageworker.ReadHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := l.sub(rel)
	if err != nil {
		return nil, err
	}
	root, err := l.root.OpenRoot(l.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if p == l.dir {
		p = "."
	} else {
		p = strings.TrimPrefix(p, l.dir+"/")
	}
	f, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("storage: not a regular file: %s", rel)
	}
	return &contextReader{ctx: ctx, File: f}, nil
}

type contextReader struct {
	ctx context.Context
	*os.File
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.File.Read(p)
}

func (r *contextReader) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.File.ReadAt(p, off)
}

func (l *local) BeginWrite(ctx context.Context, rel string, limit int64) (storageworker.AtomicWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > storageworker.MaxStreamBytes {
		return nil, fmt.Errorf("storage: invalid stream limit %d", limit)
	}
	p, err := l.sub(rel)
	if err != nil {
		return nil, err
	}
	if err := l.root.MkdirAll(l.dir, 0755); err != nil {
		return nil, err
	}
	root, err := l.root.OpenRoot(l.dir)
	if err != nil {
		return nil, err
	}
	if p == l.dir {
		p = "."
	} else {
		p = strings.TrimPrefix(p, l.dir+"/")
	}
	failed := func(err error) (storageworker.AtomicWriter, error) { _ = root.Close(); return nil, err }
	if err := refuseStreamSymlink(root, p); err != nil {
		return failed(err)
	}
	owner, _ := root.Stat(".")
	if err := mkdirStreamParents(root, filepath.Dir(p), owner); err != nil {
		return failed(err)
	}
	// Exclusive creation and unpredictable names prevent adopting another write's staging.
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return failed(err)
	}
	tmp := filepath.Join(filepath.Dir(p), fmt.Sprintf(".carbon-write-%x.partial", token))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return failed(err)
	}
	mode := os.FileMode(0644)
	if fi, err := root.Stat(p); err == nil && fi.Mode().IsRegular() {
		mode = fi.Mode().Perm()
		owner = fi
	}
	return &localWriter{root: root, ctx: ctx, file: f, target: p, tmp: tmp, limit: limit, mode: mode, owner: owner}, nil
}

func mkdirStreamParents(root *os.Root, dir string, owner os.FileInfo) error {
	if dir == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
		current = filepath.Join(current, part)
		if fi, err := root.Lstat(current); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: %q", ErrSymlink, current)
			}
			if !fi.IsDir() {
				return fmt.Errorf("storage: non-directory parent %q", current)
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := root.Mkdir(current, 0755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return err
		}
		f, err := root.Open(current)
		if err != nil {
			return err
		}
		adoptStreamOwnership(f, owner)
		_ = f.Close()
	}
	return nil
}

func refuseStreamSymlink(root *os.Root, p string) error {
	if fi, err := root.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %q", ErrSymlink, p)
	}
	return nil
}

type localWriter struct {
	root                        *os.Root
	ctx                         context.Context
	file                        *os.File
	target, tmp                 string
	limit, written              int64
	mode                        os.FileMode
	owner                       os.FileInfo
	err                         error
	closed, committed, preserve bool
}

func (w *localWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return 0, err
	}
	if int64(len(p)) > w.limit-w.written {
		w.err = fmt.Errorf("storage: stream exceeds %d byte limit", w.limit)
		return 0, w.err
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func (w *localWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	err := w.file.Close()
	if !w.committed && !w.preserve {
		err = errors.Join(err, w.root.Remove(w.tmp))
	}
	return errors.Join(err, w.root.Close())
}

func (w *localWriter) Commit() error {
	if w.committed {
		return nil
	}
	if w.closed {
		return os.ErrClosed
	}
	defer w.root.Close()
	err := w.err
	if err == nil {
		err = w.ctx.Err()
	}
	if err == nil {
		adoptStreamOwnership(w.file, w.owner)
		err = w.file.Chmod(w.mode)
	}
	if err == nil {
		err = w.file.Sync()
	}
	err = errors.Join(err, w.file.Close())
	w.closed = true
	if err == nil {
		err = w.ctx.Err()
	}
	if err == nil {
		err = refuseStreamSymlink(w.root, w.target)
	}
	if err == nil {
		err = w.root.Rename(w.tmp, w.target)
	}
	if err != nil {
		w.preserve = true
		return fmt.Errorf("storage: commit failed; recovery staging %q: %w", w.tmp, err)
	}
	w.committed = true
	return nil
}
