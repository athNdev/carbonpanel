// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package files

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrFileTooLarge is returned when a write would pass the declared size.
// ErrClosed is returned by a second Close or by Write after Close or Abort.
var (
	ErrFileTooLarge = errors.New("files: file exceeds declared size")
	ErrClosed       = errors.New("files: writer closed")
)

// AtomicWriter stages a write in a private 0700 directory beside dest and
// renames it into place on Close. The caller resolves dest, including
// ResolveInRoot, before calling NewAtomicWriter. Abort removes the staged file
// and its directory and leaves dest unchanged.
//
// The staging directory is created in the destination's own parent, so the
// final rename stays on one filesystem and is atomic. The staged file is 0600
// for the whole write; its final mode is applied only immediately before the
// rename. A new destination takes the umask-masked 0666 mode exactly as
// os.Create produced, while an existing destination keeps its previous mode
// and owner. No restrictive mode is broadened during staging.
type AtomicWriter struct {
	dest       string
	tmp        *os.File
	stagingDir string
	maxBytes   int64
	written    int64
	finalMode  os.FileMode
	done       bool

	// stagingHook is a test-only observation point for the private staged
	// path. It is nil in every production caller.
	stagingHook func(string)
}

// NewAtomicWriter creates a private staging directory in the destination's
// parent and a 0600 staged file inside it. maxBytes is the most Write will
// accept. The destination directory must exist.
func NewAtomicWriter(dest string, maxBytes int64) (*AtomicWriter, error) {
	if dest == "" {
		return nil, ErrEmptyRoot
	}
	if maxBytes < 0 {
		return nil, fmt.Errorf("files: negative size")
	}
	finalMode := os.FileMode(0o666)
	preserveMode := false
	if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() {
		finalMode = info.Mode().Perm()
		preserveMode = true
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	dir := filepath.Dir(dest)
	stagingDir, err := os.MkdirTemp(dir, ".atomic-staging-")
	if err != nil {
		return nil, err
	}
	abortDir := func(cause error) error {
		_ = os.RemoveAll(stagingDir)
		return cause
	}
	// MkdirTemp is 0700 by contract; set it explicitly so a future change to
	// that contract cannot expose partial content.
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		return nil, abortDir(err)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, abortDir(err)
	}
	stagedPath := filepath.Join(stagingDir, fmt.Sprintf("payload-%x", token))
	// Create briefly with 0666 to observe the umask-masked final mode for a
	// new destination, then tighten immediately. The surrounding 0700
	// directory keeps the file unreadable to other users even in that instant.
	tmp, err := os.OpenFile(stagedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return nil, abortDir(err)
	}
	if !preserveMode {
		if info, statErr := tmp.Stat(); statErr == nil {
			finalMode = info.Mode().Perm()
		}
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, abortDir(err)
	}
	return &AtomicWriter{
		dest: dest, tmp: tmp, stagingDir: stagingDir,
		maxBytes: maxBytes, finalMode: finalMode,
	}, nil
}

// Write rejects a chunk that would pass maxBytes and writes none of that chunk.
func (w *AtomicWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, ErrClosed
	}
	if w.written+int64(len(p)) > w.maxBytes {
		return 0, ErrFileTooLarge
	}
	n, err := w.tmp.Write(p)
	w.written += int64(n)
	if w.stagingHook != nil {
		w.stagingHook(w.tmp.Name())
	}
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, fmt.Errorf("files: short write")
	}
	return n, nil
}

// Abort removes the staged file and its private directory. It does not remove
// a destination already published by Close.
func (w *AtomicWriter) Abort() error {
	if w.done {
		return ErrClosed
	}
	w.done = true
	closeErr := w.tmp.Close()
	removeErr := os.Remove(w.tmp.Name())
	dirErr := os.Remove(w.stagingDir)
	return errors.Join(closeErr, removeErr, dirErr)
}

// Close publishes the staged file as dest with its final mode and (best
// effort) the previous owner. The mode change happens inside the private
// staging directory, immediately before the rename, so partial content is
// never exposed and a restrictive mode is never broadened during the write.
func (w *AtomicWriter) Close() error {
	if w.done {
		return ErrClosed
	}
	w.done = true
	name := w.tmp.Name()
	fail := func(cause error) error {
		_ = w.tmp.Close()
		_ = os.Remove(name)
		_ = os.Remove(w.stagingDir)
		return cause
	}
	// Ownership adoption is best effort for parity with the streaming writer:
	// a non-root controller cannot chown, but a root controller must preserve
	// the uid/gid the game container expects.
	_ = AdoptOwnership(w.dest, name, false)
	if err := w.tmp.Chmod(w.finalMode); err != nil {
		return fail(err)
	}
	if err := w.tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := w.tmp.Close(); err != nil {
		return fail(err)
	}
	if err := replaceFile(name, w.dest); err != nil {
		return fail(err)
	}
	_ = os.Remove(w.stagingDir)
	return nil
}

func replaceFile(tmp, dest string) error {
	backup := dest + ".replace"
	_ = os.Remove(backup)
	moved := false
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
		moved = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		if moved {
			_ = os.Rename(backup, dest)
		}
		return err
	}
	if moved {
		_ = os.Remove(backup)
	}
	return nil
}
