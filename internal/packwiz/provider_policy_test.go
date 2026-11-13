// SPDX-License-Identifier: MPL-2.0
package packwiz

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

type packPolicyTransport func(*http.Request) (*http.Response, error)

func (f packPolicyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func providerUpdateManager(t *testing.T, transport http.RoundTripper) *Manager {
	t.Helper()
	m := NewManager(t.TempDir(), logger.New(), ManagerOptions{HTTPClient: &http.Client{Transport: transport}, ModrinthBaseURL: "https://provider.fixture/v2"})
	if _, err := m.CreatePack(&Pack{ID: "pack", Name: "Pack", MCVersion: "1.21.1", ModLoader: "fabric"}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddContent("pack", ModItem{Kind: ContentKindMod, Slug: "mod", Name: "Mod", FileName: "mod.jar", Platform: "modrinth", ProjectID: "project", VersionID: "old", DownloadURL: "https://cdn.fixture/old.jar", HashFormat: "sha512", Hash: "old-hash"}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpdateCheckUsesSharedProviderWithoutChangingSelection(t *testing.T) {
	calls := 0
	m := providerUpdateManager(t, packPolicyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "provider.fixture" || r.URL.Path != "/v2/project/project/version" || r.URL.RawQuery != "" {
			t.Errorf("unexpected provider request: %s", r.URL)
		}
		body := `[{"id":"wrong-game","game_versions":["1.20.1"],"loaders":["fabric"],"files":[{"filename":"wrong.jar"}]},{"id":"wrong-loader","game_versions":["1.21.1"],"loaders":["forge"],"files":[{"filename":"wrong.jar"}]},{"id":"new","game_versions":["1.21.1"],"loaders":["fabric"],"files":[{"filename":"extra.jar","url":"https://cdn.fixture/extra.jar"},{"filename":"primary.jar","url":"https://cdn.fixture/primary.jar","primary":true,"hashes":{"sha512":"preferred-hash","sha1":"other-hash"}}]}]`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}))
	updates, err := m.CheckContentUpdatesContext(context.Background(), "pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || calls != 1 {
		t.Fatalf("updates=%+v calls=%d", updates, calls)
	}
	u := updates[0]
	if !u.UpdateAvailable || u.LatestVersionID != "new" || u.LatestFileName != "primary.jar" || u.LatestHashFormat != "sha512" || u.LatestHash != "preferred-hash" {
		t.Fatalf("selection changed: %+v", u)
	}
}

func TestUpdateCheckCancellationDoesNotReportFalseSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := providerUpdateManager(t, packPolicyTransport(func(r *http.Request) (*http.Response, error) { cancel(); return nil, r.Context().Err() }))
	if _, err := m.CheckContentUpdatesContext(ctx, "pack"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestAuthoringPreservesDistinctFileSelections(t *testing.T) {
	calls := 0
	m := providerUpdateManager(t, packPolicyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("loaders") != `["fabric"]` || r.URL.Query().Get("game_versions") != `["1.21.1"]` {
			t.Errorf("filters: %s", r.URL)
		}
		body := `[{"id":"new","name":"New","files":[{"filename":"first.jar","url":"https://cdn.fixture/first.jar"},{"filename":"primary.jar","url":"https://cdn.fixture/primary.jar","primary":true,"size":42}],"dependencies":[{"project_id":"required","version_id":"pinned","dependency_type":"required"},{"project_id":null,"dependency_type":"required"}]}]`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}))
	mod, deps, err := m.NewHTTPDependencyFetcher().LatestCompatibleMod("modrinth", "project", "fabric", "1.21.1")
	if err != nil || mod.FileName != "primary.jar" || mod.FileSize != 42 || len(deps) != 1 || deps[0].VersionID != "pinned" {
		t.Fatalf("dependency: %+v %+v %v", mod, deps, err)
	}
	updates, err := m.CheckModUpdatesContext(context.Background(), "pack")
	if err != nil || len(updates) != 1 || updates[0].LatestFileName != "first.jar" || !updates[0].UpdateAvailable {
		t.Fatalf("updates: %+v %v", updates, err)
	}
	report, err := m.SimulateMigrationContext(context.Background(), "pack", "1.21.1", "fabric")
	if err != nil || report.CompatibleCount != 1 || report.Mods[0].TargetFileName != "first.jar" || calls != 3 {
		t.Fatalf("migration: %+v %v calls=%d", report, err, calls)
	}
}

func TestAuthoringCancellationStopsProviderWork(t *testing.T) {
	for _, operation := range []string{"dependencies", "updates", "migration"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			m := providerUpdateManager(t, packPolicyTransport(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return nil, r.Context().Err() }))
			var err error
			switch operation {
			case "dependencies":
				_, _, err = m.NewHTTPDependencyFetcherContext(ctx).LatestCompatibleMod("modrinth", "project", "fabric", "1.21.1")
			case "updates":
				_, err = m.CheckModUpdatesContext(ctx, "pack")
			case "migration":
				_, err = m.SimulateMigrationContext(ctx, "pack", "1.21.1", "fabric")
			}
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestCurseForgeDependencyUsesSharedAdapter(t *testing.T) {
	m := NewManager(t.TempDir(), logger.New(), ManagerOptions{CurseForgeBaseURL: "https://api.curse.tools/v1/cf", HTTPClient: &http.Client{Transport: packPolicyTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Api-Key") != "" || r.URL.Query().Get("pageSize") != "50" || r.URL.Query().Get("gameVersion") != "1.21.1" {
			t.Errorf("request %+v", r)
		}
		body := `{"data":[{"id":1234567,"displayName":"First","fileName":"first.jar","gameVersions":["Forge"]},{"id":2345678,"displayName":"Last matching","fileName":"file name.jar","fileLength":99,"gameVersions":["Forge"],"dependencies":[{"modId":7,"relationType":3},{"modId":8,"relationType":2}]}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}})
	mod, deps, err := m.NewHTTPDependencyFetcher().LatestCompatibleMod("curseforge", "12", "forge", "1.21.1")
	if err != nil || mod.VersionID != "2345678" || mod.DownloadURL != "https://edge.forgecdn.net/files/2345/678/file%20name.jar" || mod.FileSize != 99 || len(deps) != 2 || deps[0].DependencyType != "required" || deps[1].DependencyType != "optional" {
		t.Fatalf("mod=%+v deps=%+v err=%v", mod, deps, err)
	}
}

func TestLegacyCompatibilityKeepsLiteralVanillaFilter(t *testing.T) {
	m := providerUpdateManager(t, packPolicyTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("loaders") != `["vanilla"]` || r.URL.Query().Get("game_versions") != `[""]` {
			t.Errorf("legacy literal filters changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
	}))
	if _, err := m.SimulateMigrationContext(context.Background(), "pack", "", "vanilla"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderUpdateChecksSkipPinnedAndKeepPackMetadata(t *testing.T) {
	calls := 0
	m := providerUpdateManager(t, packPolicyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		t.Error("pinned mod triggered a provider request")
		return nil, context.Canceled
	}))
	pinned, side := true, "client"
	if err := m.UpdateContent("pack", ContentKindMod, "mod", ContentPatch{Pinned: &pinned, Side: &side}); err != nil {
		t.Fatal(err)
	}
	before, err := m.GetPack("pack")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []func() ([]ModUpdateInfo, error){func() ([]ModUpdateInfo, error) { return m.CheckModUpdates("pack") }, func() ([]ModUpdateInfo, error) { return m.CheckContentUpdates("pack") }} {
		updates, err := check()
		if err != nil || len(updates) != 1 || !updates[0].Pinned || updates[0].UpdateAvailable {
			t.Fatalf("updates=%+v err=%v", updates, err)
		}
	}
	after, err := m.GetPack("pack")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || after.Mods[0] != before.Mods[0] {
		t.Fatalf("metadata changed: before=%+v after=%+v", before.Mods, after.Mods)
	}
}
