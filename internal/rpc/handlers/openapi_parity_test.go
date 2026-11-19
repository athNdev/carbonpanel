// SPDX-License-Identifier: MPL-2.0
package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
	"gopkg.in/yaml.v3"
)

// TestServedOpenAPIVersionMatchesBuild probes a running disposable HTTP server
// that uses the real embedded build FS. Release CI sets the expected product
// version from plan.outputs.semver after package() stages the stamped schema
// into web/carbon-panel/build/schemav1.yaml.
func TestServedOpenAPIVersionMatchesBuild(t *testing.T) {
	expected := os.Getenv("CARBONPANEL_EXPECTED_OPENAPI_VERSION")
	if expected == "" {
		t.Skip("set CARBONPANEL_EXPECTED_OPENAPI_VERSION for the release parity probe")
	}
	server := httptest.NewServer(NewOpenAPIHandler(logger.New(), func() bool { return true }))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/openapi.yaml") // #nosec G107 -- local disposable server
	if err != nil {
		t.Fatalf("GET served OpenAPI: %v", err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close served OpenAPI response body: %v", err)
		}
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read served OpenAPI: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("served OpenAPI status = %d, body = %q", response.StatusCode, body)
	}
	var document struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatalf("parse served OpenAPI: %v", err)
	}
	if document.Info.Version != expected {
		t.Fatalf("served info.version = %q, want %q", document.Info.Version, expected)
	}
	if !strings.Contains(string(body), "/carbonpanel.v1.") {
		t.Fatal("served OpenAPI lost the independent carbonpanel.v1 protocol identity")
	}
	t.Logf("HTTP %d from running disposable panel: info.version=%s, protocol=carbonpanel.v1",
		response.StatusCode, document.Info.Version)
}
