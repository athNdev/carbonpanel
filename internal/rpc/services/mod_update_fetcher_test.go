// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/pkg/utils"
	"github.com/stretchr/testify/require"
)

type versionLookupFixture struct {
	version *modrinth.Version
	err     error
}

func (f versionLookupFixture) GetVersion(context.Context, string) (*modrinth.Version, error) {
	return f.version, f.err
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

func loopbackProviderFetcher(t *testing.T, version *modrinth.Version, allowedOrigins ...string) providerModCandidateFetcher {
	t.Helper()
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	if len(allowedOrigins) == 0 {
		for _, file := range version.Files {
			allowedOrigins = append(allowedOrigins, file.URL)
		}
	}
	return providerModCandidateFetcher{
		versions: versionLookupFixture{version: version},
		policy:   utils.ProviderEgressPolicy(true, allowedOrigins...),
	}
}

type hostMapTransport struct {
	hosts map[string]string
	base  http.RoundTripper
}

func (t hostMapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if target, ok := t.hosts[clone.URL.Host]; ok {
		u, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		clone.URL.Scheme = u.Scheme
		clone.URL.Host = u.Host
	}
	return t.base.RoundTrip(clone)
}

func TestGuardedDownloadClientStripsCredentialsOnCrossOriginRedirect(t *testing.T) {
	redirectedAuthorization := make(chan string, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorization <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://cdn.modrinth.test:8443/package.jar", http.StatusFound)
	}))
	defer origin.Close()

	// Production wraps the connection-guarded client with the same redirect
	// policy. The transport only maps provider-like hosts onto local fixtures.
	client := indexers.ProtectCredentialsOnRedirect(&http.Client{
		Timeout: 2 * time.Minute,
		Transport: hostMapTransport{base: http.DefaultTransport, hosts: map[string]string{
			"cdn.modrinth.test":      origin.URL,
			"cdn.modrinth.test:8443": destination.URL,
		}},
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://cdn.modrinth.test/package.jar", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer must-not-cross-origin")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, <-redirectedAuthorization, "credentials must be stripped when only the origin port changes")
}

func TestProviderFetcherVerifiesRealBytesAndEgress(t *testing.T) {
	ctx := context.Background()

	t.Run("verified download", func(t *testing.T) {
		body := "next"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()
		version := candidateVersion(server.URL+"/mod-2.jar", candidateHash(body))
		fetcher := loopbackProviderFetcher(t, version)
		data, consumed, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash(body), "fabric", "1.21.1", maxModUpdateBytes)
		require.NoError(t, err)
		require.Empty(t, outcome)
		require.Equal(t, int64(len(body)), consumed)
		require.Equal(t, body, string(data))
	})

	t.Run("temporary release lookup failure is retryable", func(t *testing.T) {
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{err: errors.New("provider transport unavailable")}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "transient_download", outcome)
	})

	t.Run("confirmed missing release is refused", func(t *testing.T) {
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{err: indexers.NewAPIError("modrinth", 404, "https://api.modrinth.test/v2/version/update", "")}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("rate-limited download is retryable", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer provider.Close()
		version := candidateVersion(provider.URL+"/mod-2.jar", candidateHash("next"))
		fetcher := loopbackProviderFetcher(t, version)
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "transient_download", outcome)
	})

	t.Run("real checksum mismatch is refused", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("tampered"))
		}))
		defer server.Close()
		version := candidateVersion(server.URL+"/mod-2.jar", candidateHash("next"))
		fetcher := loopbackProviderFetcher(t, version)
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_checksum", outcome)
	})

	t.Run("literal-IP download URL is refused", func(t *testing.T) {
		version := candidateVersion("http://127.0.0.1:65534/mod-2.jar", candidateHash("next"))
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_download", outcome)
	})

	t.Run("unlisted or incompatible release is refused", func(t *testing.T) {
		version := candidateVersion("https://cdn.modrinth.com/files/mod-2.jar", candidateHash("next"))
		version.Status = "draft"
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("ambiguous reviewed files are refused", func(t *testing.T) {
		version := candidateVersion("https://cdn.modrinth.com/files/mod-2.jar", candidateHash("next"))
		version.Files = append(version.Files, version.Files[0])
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("reviewed checksum must be a SHA-512 digest", func(t *testing.T) {
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: candidateVersion("https://cdn.modrinth.com/files/mod-2.jar", "short")}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", "short", "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.True(t, strings.Contains(outcome, "refused_provider"), outcome)
	})
}

func TestProviderFetcherEnforcesEgressPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("download host outside provider allowlist is refused", func(t *testing.T) {
		version := candidateVersion("https://evil.example/files/mod-2.jar", candidateHash("next"))
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_download", outcome)
	})

	t.Run("redirect to host outside provider allowlist is refused", func(t *testing.T) {
		var destinationHits int
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			destinationHits++
			_, _ = w.Write([]byte("should-not-reach"))
		}))
		defer destination.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Same loopback IP, different hostname string: host-allowlist
			// enforcement must reject the redirect before it is followed.
			http.Redirect(w, r, strings.Replace(destination.URL, "127.0.0.1", "localhost", 1)+"/mod-2.jar", http.StatusFound)
		}))
		defer origin.Close()

		version := candidateVersion(origin.URL+"/mod-2.jar", candidateHash("next"))
		// Only the origin is allowlisted; the redirect target is another host
		// and must be refused by the same egress policy.
		fetcher := loopbackProviderFetcher(t, version, origin.URL)
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_download", outcome)
		require.Zero(t, destinationHits, "redirect target must not receive the request")
	})

	t.Run("env proxy cannot bypass provider egress policy", func(t *testing.T) {
		body := "next"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()
		t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
		version := candidateVersion(server.URL+"/mod-2.jar", candidateHash(body))
		fetcher := loopbackProviderFetcher(t, version)
		data, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash(body), "fabric", "1.21.1", maxModUpdateBytes)
		require.NoError(t, err)
		require.Empty(t, outcome)
		require.Equal(t, body, string(data))
	})
}
