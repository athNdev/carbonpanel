// SPDX-License-Identifier: MPL-2.0
package modrinth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/indexers"
)

type policyTransport func(*http.Request) (*http.Response, error)

func (f policyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSharedClientPreservesCallerRequestAndProviderOrder(t *testing.T) {
	var requests []*http.Request
	transport := policyTransport(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r)
		if r.Header.Get("Authorization") != "fixture-token" || r.Header.Get("User-Agent") != "fixture-agent" {
			t.Error("caller headers lost")
		}
		body := `{"hits":[]}`
		if strings.HasSuffix(r.URL.Path, "/version") {
			body = `[{"id":"first","files":[{"filename":"primary.jar","primary":true,"hashes":{"sha512":"hash"}}]},{"id":"second"}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client := NewClientWithOptions(nil, Options{BaseURL: "https://provider.fixture/v2/", Token: "fixture-token", UserAgent: "fixture-agent", HTTPClient: &http.Client{Transport: transport}})
	_, err := client.Search(context.Background(), SearchOptions{ProjectType: "mod", Query: "a & b", Loader: "fabric", GameVersion: "1.21.1", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	query := requests[0].URL.Query()
	if query.Get("query") != "a & b" || query.Get("limit") != "25" || query.Get("index") != "" || query.Get("facets") != `[["project_type:mod"],["versions:1.21.1"],["categories:fabric"]]` {
		t.Fatalf("search policy changed: %s", requests[0].URL)
	}
	versions, err := client.GetVersions(context.Background(), "project", "fabric", "1.21.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].ID != "first" || versions[1].ID != "second" || versions[0].Files[0].Hashes.SHA512 != "hash" {
		t.Fatalf("provider order/metadata changed: %+v", versions)
	}
	if requests[1].URL.Query().Get("loaders") != `["fabric"]` || requests[1].URL.Query().Get("game_versions") != `["1.21.1"]` {
		t.Fatalf("version filters lost: %s", requests[1].URL)
	}
}

func TestSharedClientReturnsStructuredFailuresAndCancellation(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewClientWithOptions(nil, Options{HTTPClient: &http.Client{Transport: policyTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("malformed")), Header: make(http.Header)}, nil
			})}})
			_, err := client.GetVersions(context.Background(), "project", "", "")
			var failure *indexers.IndexerError
			if !errors.As(err, &failure) {
				t.Fatalf("unstructured failure: %v", err)
			}
			if status == 200 && failure.Kind != indexers.ErrDecode {
				t.Fatalf("malformed JSON: %v", err)
			}
			if status != 200 && failure.StatusCode != status {
				t.Fatalf("status lost: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClientWithOptions(nil, Options{HTTPClient: &http.Client{Transport: policyTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}})
	if _, err := client.GetVersions(ctx, "project", "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
