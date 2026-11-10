// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractZipWritesOnlyThroughAccessor(t *testing.T) {
	acc, root := testAccessor(t)
	if err := acc.WriteFile("pack.zip", zipBytes(t, map[string]string{
		"mods/a.jar": "jar-a",
		"readme.txt": "hi",
	})); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "controller-sibling")
	n, err := ExtractZip(context.Background(), acc, "pack.zip", DestRelFromArchive("pack.zip"), nil)
	if err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	if n != 2 {
		t.Fatalf("extracted %d, want 2", n)
	}
	got, err := acc.ReadFile("pack/mods/a.jar")
	if err != nil || string(got) != "jar-a" {
		t.Fatalf("pack/mods/a.jar = %q, %v", got, err)
	}
	got, err = acc.ReadFile("pack/readme.txt")
	if err != nil || string(got) != "hi" {
		t.Fatalf("pack/readme.txt = %q, %v", got, err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("extract must not create %s: %v", outside, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "pack")); !os.IsNotExist(err) {
		t.Fatalf("extract must not write beside the accessor root: %v", err)
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	acc, root := testAccessor(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("no")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := acc.WriteFile("bad.zip", buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractZip(context.Background(), acc, "bad.zip", "out", nil); err == nil || !IsEscape(err) {
		t.Fatalf("want escape rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside the accessor root: %v", err)
	}
}

func TestDestRelFromArchive(t *testing.T) {
	if got := DestRelFromArchive("mods/pack.tar.gz"); got != "mods/pack" {
		t.Fatalf("got %q", got)
	}
	if got := DestRelFromArchive("a.zip"); got != "a" {
		t.Fatalf("got %q", got)
	}
}
