// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type modPreviewFixture struct {
	identified    []string
	loader, game  string
	current, next modrinth.Version
	project       modrinth.ProjectDetails
	versionErr    error
	compatErr     error
	projectErr    error
}

func (f *modPreviewFixture) VersionsByFileHash(_ context.Context, hashes []string) (map[string]modrinth.Version, error) {
	f.identified = append(f.identified, hashes...)
	if f.versionErr != nil {
		return nil, f.versionErr
	}
	return map[string]modrinth.Version{f.current.Files[0].Hashes.SHA512: f.current}, nil
}
func (f *modPreviewFixture) CompatibleUpdatesByFileHash(_ context.Context, _ []string, loader, game string) (map[string]modrinth.Version, error) {
	f.loader, f.game = loader, game
	if f.compatErr != nil {
		return nil, f.compatErr
	}
	return map[string]modrinth.Version{f.current.Files[0].Hashes.SHA512: f.next}, nil
}
func (f *modPreviewFixture) GetProjects(context.Context, []string) ([]modrinth.ProjectDetails, error) {
	if f.projectErr != nil {
		return nil, f.projectErr
	}
	return []modrinth.ProjectDetails{f.project}, nil
}
func newPreviewFixture() *modPreviewFixture {
	f := &modPreviewFixture{project: modrinth.ProjectDetails{ID: "project", ServerSide: "required"}}
	f.current = modrinth.Version{ID: "installed", ProjectID: "project", VersionNumber: "1", DatePublished: "2025-01-01T00:00:00Z", VersionType: "release", Status: "listed", Loaders: []string{"fabric"}, GameVersions: []string{"1.21.1"}, Files: []modrinth.File{{Filename: "mod.jar", Hashes: modrinth.Hashes{SHA512: fmt.Sprintf("%x", sha512.Sum512([]byte("installed")))}}}}
	f.next = f.current
	f.next.ID, f.next.VersionNumber, f.next.DatePublished = "update", "2", "2025-02-01T00:00:00Z"
	f.next.Files = []modrinth.File{{Filename: "mod-2.jar", Hashes: modrinth.Hashes{SHA512: fmt.Sprintf("%x", sha512.Sum512([]byte("next")))}}}
	return f
}
func TestModUpdatePreviewUsesEnabledAndDisabledFileBytes(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"mods", "mods_disabled"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "mod.jar"), []byte("installed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "mods", "alias.jar"), []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	mods := []*v1.Mod{{FileName: "mod.jar", Enabled: true}, {FileName: "mod.jar"}, {FileName: "alias.jar", Enabled: true}}
	server := &db.Server{ModLoader: db.ModLoaderFabric, ModpackLoader: db.ModLoaderForge, MCVersion: "1.21.1"}
	fixture := newPreviewFixture()
	if err := previewServerModUpdates(context.Background(), root, filepath.Join(root, "mods"), server, mods, fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.identified) != 2 || fixture.loader != "fabric" || fixture.game != "1.21.1" {
		t.Fatalf("lookup %+v", fixture)
	}
	for i, want := range []string{"available", "available", "unknown_source"} {
		if mods[i].UpdatePreview.State != want {
			t.Fatalf("row %d: %+v", i, mods[i].UpdatePreview)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := previewServerModUpdates(ctx, root, filepath.Join(root, "mods"), server, mods, fixture); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preview: %v", err)
	}
}
func TestModUpdatePreviewReportsProviderLookupFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "mods"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mods", "mod.jar"), []byte("installed"), 0600); err != nil {
		t.Fatal(err)
	}
	server := &db.Server{ModLoader: db.ModLoaderFabric, MCVersion: "1.21.1"}
	const privateDiagnostic = `GET https://indexer.example.invalid/v2/version_file: 500 response body {"detail":"internal policy detail"}`
	cases := []struct {
		name  string
		setup func(*modPreviewFixture)
	}{
		{"file lookup", func(f *modPreviewFixture) { f.versionErr = errors.New(privateDiagnostic) }},
		{"compatible release lookup", func(f *modPreviewFixture) { f.compatErr = errors.New(privateDiagnostic) }},
		{"project lookup", func(f *modPreviewFixture) { f.projectErr = errors.New(privateDiagnostic) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mods := []*v1.Mod{{FileName: "mod.jar", Enabled: true}}
			fixture := newPreviewFixture()
			test.setup(fixture)
			if err := previewServerModUpdates(context.Background(), root, filepath.Join(root, "mods"), server, mods, fixture); err != nil {
				t.Fatal(err)
			}
			preview := mods[0].UpdatePreview
			if preview.State != "unavailable" || preview.Reason != providerLookupReason {
				t.Fatalf("preview %+v", preview)
			}
			if strings.Contains(preview.Reason, "indexer.example.invalid") || strings.Contains(preview.Reason, "internal policy detail") {
				t.Fatalf("preview leaks provider diagnostic: %q", preview.Reason)
			}
		})
	}
}

func TestModUpdatePreviewClassifications(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*modPreviewFixture)
	}{
		{"current", "current", func(f *modPreviewFixture) { f.next = f.current }},
		{"older release", "review_required", func(f *modPreviewFixture) { f.next.DatePublished = f.current.DatePublished }},
		{"wrong game", "review_required", func(f *modPreviewFixture) { f.next.GameVersions = nil }},
		{"client only", "review_required", func(f *modPreviewFixture) { f.project.ServerSide = "unsupported" }},
		{"ambiguous JARs", "review_required", func(f *modPreviewFixture) {
			f.next.Files[0].Primary = true
			f.next.Files = append(f.next.Files, f.next.Files[0])
		}},
		{"dependencies", "review_required", func(f *modPreviewFixture) { f.next.Dependencies = []modrinth.Dependency{{DependencyType: "required"}} }},
		{"unlisted", "review_required", func(f *modPreviewFixture) { f.next.Status = "draft" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newPreviewFixture()
			test.change(f)
			result := &v1.InstalledModUpdate{InstalledSha512: f.current.Files[0].Hashes.SHA512}
			classifyModUpdate(result, f.current, f.next, f.project, "fabric", "1.21.1", nil)
			if result.State != test.want {
				t.Fatalf("result %+v", result)
			}
		})
	}
}
