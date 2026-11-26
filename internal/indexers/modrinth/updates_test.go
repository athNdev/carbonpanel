// SPDX-License-Identifier: MPL-2.0
package modrinth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestInstalledModBatchRequests(t *testing.T) {
	calls := 0
	client := NewClientWithOptions(nil, Options{BaseURL: "https://test.invalid/v2", Token: "fixture-token", HTTPClient: &http.Client{Transport: policyTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "POST" || req.Header.Get("Authorization") != "fixture-token" || req.Header.Get("Content-Type") != "application/json" {
			t.Fatal("wrong request method or headers")
		}
		var body struct {
			Algorithm    string
			Hashes       []string
			Loaders      []string
			GameVersions []string `json:"game_versions"`
			VersionTypes []string `json:"version_types"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Algorithm != "sha512" || !slices.Equal(body.Hashes, []string{"fixture-hash"}) {
			t.Fatalf("unexpected lookup %+v", body)
		}
		switch req.URL.Path {
		case "/v2/version_files":
			if len(body.Loaders) != 0 {
				t.Fatal("unexpected filters on identity lookup")
			}
		case "/v2/version_files/update":
			if !slices.Equal(body.Loaders, []string{"fabric"}) || !slices.Equal(body.GameVersions, []string{"1.21.1"}) || !slices.Equal(body.VersionTypes, []string{"release"}) {
				t.Fatalf("unexpected filters %+v", body)
			}
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"fixture-hash":{"id":"version-id"}}`))}, nil
	})}})
	ids := []string{"fixture-hash"}
	installed, err := client.VersionsByFileHash(context.Background(), ids)
	if err != nil || installed[ids[0]].ID != "version-id" {
		t.Fatalf("lookup %v: %v", installed, err)
	}
	next, err := client.CompatibleUpdatesByFileHash(context.Background(), ids, "fabric", "1.21.1")
	if err != nil || next[ids[0]].ID != "version-id" || calls != 2 {
		t.Fatalf("updates %v: %v", next, err)
	}
}
