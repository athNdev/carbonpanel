// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package storageworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Session is a bidirectional JSONL channel to one accessor worker process.
type Session struct {
	mu        sync.Mutex
	enc       *json.Encoder
	dec       *json.Decoder
	closed    atomic.Bool
	closeOnce sync.Once
	closeF    func() error
	done      chan struct{}
	root      string
}

// NewSession starts a worker session. closeF is invoked on Close (typically
// waits on the worker process).
func NewSession(in io.Writer, out io.Reader, closeF func() error) (*Session, error) {
	return NewSessionContext(context.Background(), in, out, closeF)
}

// NewSessionContext binds worker startup and lifetime to the operation context.
// closeF must close the underlying input and output so blocked I/O can return.
func NewSessionContext(ctx context.Context, in io.Writer, out io.Reader, closeF func() error) (*Session, error) {
	s := &Session{
		enc:    json.NewEncoder(in),
		dec:    json.NewDecoder(bufio.NewReader(out)),
		closeF: closeF,
		done:   make(chan struct{}),
	}
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = s.Close()
			case <-s.done:
			}
		}()
	}
	res, err := s.call(Request{Op: "root"})
	if err != nil {
		_ = s.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if !res.OK {
		_ = s.Close()
		return nil, errors.New(res.Error)
	}
	if err := ctx.Err(); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.root = res.Root
	return s, nil
}

func (s *Session) call(req Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return Response{}, errors.New("storageworker: session closed")
	}
	if err := s.enc.Encode(req); err != nil {
		return Response{}, err
	}
	var res Response
	if err := s.dec.Decode(&res); err != nil {
		return Response{}, err
	}
	return res, nil
}

func (s *Session) callOK(req Request) (Response, error) {
	res, err := s.call(req)
	if err != nil {
		return res, err
	}
	if !res.OK {
		if res.Error == "" {
			res.Error = "worker refused operation"
		}
		return res, errors.New(res.Error)
	}
	return res, nil
}

// Root returns the worker's confined data root.
func (s *Session) Root() string { return s.root }

// Close ends the worker session.
func (s *Session) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
		if s.closeF != nil {
			closeErr = s.closeF()
		}
	})
	return closeErr
}

// RemoteAccessor implements nodestorage.Accessor over a worker session.
type RemoteAccessor struct {
	s *Session
}

// NewRemoteAccessor wraps a live worker session.
func NewRemoteAccessor(s *Session) *RemoteAccessor {
	return &RemoteAccessor{s: s}
}

func (r *RemoteAccessor) Root() string { return r.s.Root() }

func (r *RemoteAccessor) Close() error { return r.s.Close() }

func (r *RemoteAccessor) Stat(rel string) (os.FileInfo, error) {
	res, err := r.s.callOK(Request{Op: "stat", Path: rel})
	if err != nil {
		return nil, err
	}
	return remoteFileInfo{info: res.FileInfo}, nil
}

func (r *RemoteAccessor) ReadFile(rel string) ([]byte, error) {
	res, err := r.s.callOK(Request{Op: "read", Path: rel})
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

func (r *RemoteAccessor) WriteFile(rel string, data []byte) error {
	_, err := r.s.callOK(Request{Op: "write", Path: rel, Data: data})
	return err
}

func (r *RemoteAccessor) ReadDir(rel string) ([]os.DirEntry, error) {
	res, err := r.s.callOK(Request{Op: "readdir", Path: rel})
	if err != nil {
		return nil, err
	}
	entries := make([]os.DirEntry, len(res.Entries))
	for i, e := range res.Entries {
		entries[i] = remoteDirEntry{name: e.Name, isDir: e.IsDir}
	}
	return entries, nil
}

func (r *RemoteAccessor) MkdirAll(rel string) error {
	_, err := r.s.callOK(Request{Op: "mkdir", Path: rel})
	return err
}

func (r *RemoteAccessor) Remove(rel string) error {
	_, err := r.s.callOK(Request{Op: "remove", Path: rel})
	return err
}

func (r *RemoteAccessor) RemoveAll(rel string) error {
	_, err := r.s.callOK(Request{Op: "removeall", Path: rel})
	return err
}

func (r *RemoteAccessor) Rename(oldRel, newRel string) error {
	_, err := r.s.callOK(Request{Op: "rename", Path: oldRel, Path2: newRel})
	return err
}

type remoteFileInfo struct {
	info *FileInfo
}

func (f remoteFileInfo) Name() string       { return f.info.Name }
func (f remoteFileInfo) Size() int64        { return f.info.Size }
func (f remoteFileInfo) Mode() os.FileMode  { return os.FileMode(f.info.Mode) }
func (f remoteFileInfo) ModTime() time.Time { return time.Unix(0, f.info.ModTime) }
func (f remoteFileInfo) IsDir() bool        { return f.info.IsDir }
func (f remoteFileInfo) Sys() any           { return nil }

type remoteDirEntry struct {
	name  string
	isDir bool
}

func (e remoteDirEntry) Name() string      { return e.name }
func (e remoteDirEntry) IsDir() bool       { return e.isDir }
func (e remoteDirEntry) Type() os.FileMode { return 0 }
func (e remoteDirEntry) Info() (os.FileInfo, error) {
	return nil, fmt.Errorf("storageworker: Info not supported for %q", e.name)
}
