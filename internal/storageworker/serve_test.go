// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package storageworker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeFS struct {
	root string
}

func (f *fakeFS) Root() string { return f.root }
func (f *fakeFS) Stat(rel string) (os.FileInfo, error) {
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	return os.Stat(p)
}
func (f *fakeFS) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
}
func (f *fakeFS) WriteFile(rel string, data []byte) error {
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
func (f *fakeFS) ReadDir(rel string) ([]os.DirEntry, error) {
	return os.ReadDir(filepath.Join(f.root, filepath.FromSlash(rel)))
}
func (f *fakeFS) MkdirAll(rel string) error {
	return os.MkdirAll(filepath.Join(f.root, filepath.FromSlash(rel)), 0o755)
}
func (f *fakeFS) Remove(rel string) error {
	return os.Remove(filepath.Join(f.root, filepath.FromSlash(rel)))
}
func (f *fakeFS) RemoveAll(rel string) error {
	return os.RemoveAll(filepath.Join(f.root, filepath.FromSlash(rel)))
}
func (f *fakeFS) Rename(oldRel, newRel string) error {
	o := filepath.Join(f.root, filepath.FromSlash(oldRel))
	n := filepath.Join(f.root, filepath.FromSlash(newRel))
	if err := os.MkdirAll(filepath.Dir(n), 0o755); err != nil {
		return err
	}
	return os.Rename(o, n)
}

func (f *fakeFS) Close() error { return nil }

func TestAccessorWorkerRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fs := &fakeFS{root: root}
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	if err := enc.Encode(Request{Op: "write", Path: "world/level.dat", Data: []byte("lvl")}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(Request{Op: "read", Path: "world/level.dat"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ServeAccessor(strings.NewReader(in.String()), &out, fs); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	var w, r Response
	if err := dec.Decode(&w); err != nil || !w.OK {
		t.Fatalf("write: %#v %v", w, err)
	}
	if err := dec.Decode(&r); err != nil || !r.OK || string(r.Data) != "lvl" {
		t.Fatalf("read: %#v %v", r, err)
	}
}

func TestRemoteFileInfo(t *testing.T) {
	fi := remoteFileInfo{info: &FileInfo{Name: "a", Size: 3, ModTime: time.Now().UnixNano(), Mode: 0o644}}
	if fi.Name() != "a" || fi.Size() != 3 {
		t.Fatalf("file info stub: %+v", fi)
	}
	_ = fmt.Sprintf("%v", fi.ModTime())
}
