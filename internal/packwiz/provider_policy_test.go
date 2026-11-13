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
