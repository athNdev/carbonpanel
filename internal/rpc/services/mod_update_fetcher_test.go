// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"crypto/sha512"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/stretchr/testify/require"
)

type versionLookupFixture struct {
	version *modrinth.Version
	err     error
}

func (f versionLookupFixture) GetVersion(context.Context, string) (*modrinth.Version, error) {
	return f.version, f.err
}

type hostRewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func candidateVersion(downloadURL, hash string) *modrinth.Version {
	return &modrinth.Version{
		ID: "update", ProjectID: "project", Status: "listed", VersionType: "release",
		Loaders: []string{"fabric"}, GameVersions: []string{"1.21.1"},
		Files: []modrinth.File{{
			Filename: "mod-2.jar", URL: downloadURL,
			Hashes: modrinth.Hashes{SHA512: hash},
		}},
	}
}

func candidateHash(body string) string { return fmt.Sprintf("%x", sha512.Sum512([]byte(body))) }

func fetcherAgainstServer(t *testing.T, body string, version *modrinth.Version) providerModCandidateFetcher {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	return providerModCandidateFetcher{
		versions: versionLookupFixture{version: version},
		client: &http.Client{Transport: hostRewriteTransport{
			base: http.DefaultTransport, target: target,
		}},
	}
}

func TestProviderFetcherVerifiesRealBytesAndEgress(t *testing.T) {
	const allowedURL = "https://cdn.modrinth.test/files/mod-2.jar"
	ctx := context.Background()

	t.Run("verified download", func(t *testing.T) {
		fetcher := fetcherAgainstServer(t, "next", candidateVersion(allowedURL, candidateHash("next")))
		data, outcome, err := fetcher.Fetch(ctx, nil, "update", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1")
		require.NoError(t, err)
		require.Empty(t, outcome)
		require.Equal(t, "next", string(data))
	})

	t.Run("real checksum mismatch is refused", func(t *testing.T) {
		fetcher := fetcherAgainstServer(t, "tampered", candidateVersion(allowedURL, candidateHash("next")))
		_, outcome, err := fetcher.Fetch(ctx, nil, "update", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1")
		require.Error(t, err)
		require.Equal(t, "refused_checksum", outcome)
	})

	t.Run("literal-IP download URL is refused", func(t *testing.T) {
		version := candidateVersion("http://127.0.0.1:65534/mod-2.jar", candidateHash("next"))
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, outcome, err := fetcher.Fetch(ctx, nil, "update", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1")
		require.Error(t, err)
		require.Equal(t, "refused_download", outcome)
	})

	t.Run("unlisted or incompatible release is refused", func(t *testing.T) {
		version := candidateVersion(allowedURL, candidateHash("next"))
		version.Status = "draft"
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, outcome, err := fetcher.Fetch(ctx, nil, "update", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1")
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("ambiguous reviewed files are refused", func(t *testing.T) {
		version := candidateVersion(allowedURL, candidateHash("next"))
		version.Files = append(version.Files, version.Files[0])
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, outcome, err := fetcher.Fetch(ctx, nil, "update", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1")
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("reviewed checksum must be a SHA-512 digest", func(t *testing.T) {
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: candidateVersion(allowedURL, "short")}}
		_, outcome, err := fetcher.Fetch(ctx, nil, "update", "mod-2.jar", "short", "fabric", "1.21.1")
		require.Error(t, err)
		require.True(t, strings.Contains(outcome, "refused_provider"), outcome)
	})
}
