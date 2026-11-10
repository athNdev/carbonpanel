// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package storageworker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const maxReadBytes = 128 << 20

// FS exposes the nodestorage.Accessor surface without importing nodestorage
// (the worker binary and remote client share this package).
type FS interface {
	Stat(rel string) (os.FileInfo, error)
	ReadFile(rel string) ([]byte, error)
	WriteFile(rel string, data []byte) error
	ReadDir(rel string) ([]os.DirEntry, error)
	MkdirAll(rel string) error
	Remove(rel string) error
	RemoveAll(rel string) error
	Rename(oldRel, newRel string) error
	Root() string
}

// ServeAccessor runs the accessor worker loop on in and writes responses to out.
func ServeAccessor(in io.Reader, out io.Writer, fs FS) error {
	return serveAccessor(in, out, fs)
}

func serveAccessor(in io.Reader, out io.Writer, fs FS) (err error) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), maxReadBytes+1<<20)
	w := bufio.NewWriter(out)
	defer func() {
		if ferr := w.Flush(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		var req Request
		if uerr := json.Unmarshal(scanner.Bytes(), &req); uerr != nil {
			if e := enc.Encode(Response{Error: uerr.Error()}); e != nil {
				return e
			}
			continue
		}
		res := dispatch(fs, req)
		if e := enc.Encode(res); e != nil {
			return e
		}
		if e := w.Flush(); e != nil {
			return e
		}
	}
	return scanner.Err()
}

func dispatch(fs FS, r Request) Response {
	switch r.Op {
	case "root":
		return Response{OK: true, Root: fs.Root()}
	case "stat":
		fi, err := fs.Stat(r.Path)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, FileInfo: fileInfoFromOS(fi)}
	case "read":
		data, err := fs.ReadFile(r.Path)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Data: data}
	case "write":
		if err := fs.WriteFile(r.Path, r.Data); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	case "readdir":
		entries, err := fs.ReadDir(r.Path)
		if err != nil {
			return Response{Error: err.Error()}
		}
		out := make([]DirEntry, 0, len(entries))
		for _, e := range entries {
			out = append(out, DirEntry{Name: e.Name(), IsDir: e.IsDir()})
		}
		return Response{OK: true, Entries: out}
	case "mkdir":
		if err := fs.MkdirAll(r.Path); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	case "remove":
		if err := fs.Remove(r.Path); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	case "removeall":
		if err := fs.RemoveAll(r.Path); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	case "rename":
		if err := fs.Rename(r.Path, r.Path2); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	default:
		return Response{Error: fmt.Sprintf("unsupported operation %q", r.Op)}
	}
}

func fileInfoFromOS(fi os.FileInfo) *FileInfo {
	return &FileInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		IsDir:   fi.IsDir(),
		ModTime: fi.ModTime().UnixNano(),
		Mode:    uint32(fi.Mode()),
	}
}
