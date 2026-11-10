// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package storageworker implements the line-oriented JSON protocol spoken by
// cmd/carbon-storage in accessor mode and consumed by remote nodestorage
// adapters (OSS-281).
package storageworker

// Request is one JSON line sent to the worker.
type Request struct {
	Op      string `json:"op"`
	Path    string `json:"path,omitempty"`
	Path2   string `json:"path2,omitempty"`
	Data    []byte `json:"data,omitempty"`
	Perms   uint32 `json:"perms,omitempty"`
	ModTime int64  `json:"mod_time,omitempty"`
}

// DirEntry is a directory listing entry returned by readdir.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// FileInfo is the stat payload for a single path.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"`
	Mode    uint32 `json:"mode"`
}

// Response is one JSON line from the worker.
type Response struct {
	OK       bool       `json:"ok"`
	Error    string     `json:"error,omitempty"`
	Data     []byte     `json:"data,omitempty"`
	Entries  []DirEntry `json:"entries,omitempty"`
	FileInfo *FileInfo  `json:"file_info,omitempty"`
	Root     string     `json:"root,omitempty"`
}
