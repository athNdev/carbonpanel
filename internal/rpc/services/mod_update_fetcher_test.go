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

func fetcherAgainstStatus(t *testing.T, status int, version *modrinth.Version) providerModCandidateFetcher {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
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
	const allowedURL = "https://cdn.modrinth.test/files/mod-2.jar"
	ctx := context.Background()

	t.Run("verified download", func(t *testing.T) {
		fetcher := fetcherAgainstServer(t, "next", candidateVersion(allowedURL, candidateHash("next")))
		data, consumed, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.NoError(t, err)
		require.Empty(t, outcome)
		require.Equal(t, int64(len("next")), consumed)
		require.Equal(t, "next", string(data))
	})

	t.Run("real checksum mismatch is refused", func(t *testing.T) {
		fetcher := fetcherAgainstServer(t, "tampered", candidateVersion(allowedURL, candidateHash("next")))
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

	t.Run("download timeout and rate-limit statuses are transient", func(t *testing.T) {
		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests} {
			fetcher := fetcherAgainstStatus(t, status, candidateVersion(allowedURL, candidateHash("next")))
			_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
			require.Error(t, err)
			require.Equal(t, "transient_download", outcome, "HTTP %d must be retried", status)
		}
		fetcher := fetcherAgainstStatus(t, http.StatusBadRequest, candidateVersion(allowedURL, candidateHash("next")))
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_download", outcome, "a definitive 4xx stays a refusal")
	})

	t.Run("unlisted or incompatible release is refused", func(t *testing.T) {
		version := candidateVersion(allowedURL, candidateHash("next"))
		version.Status = "draft"
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("ambiguous reviewed files are refused", func(t *testing.T) {
		version := candidateVersion(allowedURL, candidateHash("next"))
		version.Files = append(version.Files, version.Files[0])
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("reviewed checksum must be a SHA-512 digest", func(t *testing.T) {
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: candidateVersion(allowedURL, "short")}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", "short", "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.True(t, strings.Contains(outcome, "refused_provider"), outcome)
	})
}

func TestProviderFetcherClassifiesReleaseLookupFailures(t *testing.T) {
	const allowedURL = "https://cdn.modrinth.test/files/mod-2.jar"
	ctx := context.Background()
	cases := []struct {
		name    string
		err     error
		outcome string
	}{
		{"network timeout", indexers.NewNetworkError("modrinth", "https://api.modrinth.test", errors.New("timeout")), "transient_download"},
		{"rate limit", indexers.NewAPIError("modrinth", http.StatusTooManyRequests, "https://api.modrinth.test", ""), "transient_download"},
		{"server failure", indexers.NewAPIError("modrinth", http.StatusServiceUnavailable, "https://api.modrinth.test", ""), "transient_download"},
		{"decode failure", indexers.NewDecodeError("modrinth", "https://api.modrinth.test", errors.New("bad json")), "transient_download"},
		{"context deadline", fmt.Errorf("lookup: %w", context.DeadlineExceeded), "transient_download"},
		{"not found", indexers.NewAPIError("modrinth", http.StatusNotFound, "https://api.modrinth.test", ""), "refused_provider"},
		{"gone", indexers.NewAPIError("modrinth", http.StatusGone, "https://api.modrinth.test", ""), "refused_provider"},
		{"invalid request", indexers.NewAPIError("modrinth", http.StatusBadRequest, "https://api.modrinth.test", ""), "refused_provider"},
		{"unauthenticated", indexers.NewAuthConfigError("modrinth", "missing token"), "refused_provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := providerModCandidateFetcher{versions: versionLookupFixture{err: tc.err}}
			_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
			require.Error(t, err)
			require.Equal(t, tc.outcome, outcome)
		})
	}

	t.Run("missing release is definitive", func(t *testing.T) {
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{}}
		_, _, outcome, err := fetcher.Fetch(ctx, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "refused_provider", outcome)
	})

	t.Run("canceled context is transient", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: candidateVersion(allowedURL, candidateHash("next"))}}
		_, _, outcome, err := fetcher.Fetch(canceled, nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
		require.Error(t, err)
		require.Equal(t, "transient_download", outcome)
	})
}

func TestProviderFetcherRefusesDependencyAddedAfterReview(t *testing.T) {
	const allowedURL = "https://cdn.modrinth.test/files/mod-2.jar"
	version := candidateVersion(allowedURL, candidateHash("next"))
	version.Dependencies = []modrinth.Dependency{{DependencyType: "required"}}
	fetcher := providerModCandidateFetcher{versions: versionLookupFixture{version: version}}

	_, _, outcome, err := fetcher.Fetch(context.Background(), nil, "update", "project", "mod-2.jar", candidateHash("next"), "fabric", "1.21.1", maxModUpdateBytes)
	require.Error(t, err)
	require.Equal(t, "refused_provider", outcome)
	require.Contains(t, err.Error(), "dependencies changed since review")
}
