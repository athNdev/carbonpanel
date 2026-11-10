// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// swallowNoClient matches a Locator caller that turns ErrNoClient back into
// the (nil, nil) adapter the locator replaced.
var swallowNoClient = regexp.MustCompile(`errors\.Is\(err,\s*(docker\.)?ErrNoClient\)[\s\S]{0,240}?return nil, nil`)

// TestLocatorCallersDoNotSwallowNoClient is the deletion test for leftover
// node-to-daemon shims: translating ErrNoClient into (nil, nil) must not
// return. Removing those two adapters is what made this pass.
func TestLocatorCallersDoNotSwallowNoClient(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	root := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}

	var leftovers []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if swallowNoClient.Find(body) == nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		leftovers = append(leftovers, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) > 0 {
		t.Fatalf("locator callers still swallow ErrNoClient as (nil, nil): %s", strings.Join(leftovers, ", "))
	}
}
