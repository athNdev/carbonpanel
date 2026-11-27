// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func fabricOptimizationFixture(t *testing.T, handler http.HandlerFunc, targetName string) (string, modrinthVersionFile) {
	t.Helper()
	mods := t.TempDir()
	existing := filepath.Join(mods, "lithium-1.0.jar")
	require.NoError(t, os.WriteFile(existing, []byte("installed"), 0o600))
	fileServer := httptest.NewServer(handler)
	t.Cleanup(fileServer.Close)
	return existing, modrinthVersionFile{URL: fileServer.URL, Filename: targetName}
}

func TestFabricOptimizationInstallPreservesJarOnNon2xx(t *testing.T) {
	existing, target := fabricOptimizationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}, "lithium-2.0.jar")

	err := installFabricOptimizationJar(context.Background(), filepath.Dir(existing), target, "lithium")
	require.Error(t, err)
	body, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "a non-2xx response must not replace the installed jar")
	require.NoFileExists(t, filepath.Join(filepath.Dir(existing), "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, filepath.Dir(existing))
}

// A 206 response is a declared fragment, not the complete jar, even when its
// Content-Length matches the bytes it sends. It must not replace the installed
// jar.
func TestFabricOptimizationInstallRejectsPartialContent(t *testing.T) {
	existing, target := fabricOptimizationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		body := []byte("fragment")
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}, "lithium-2.0.jar")

	err := installFabricOptimizationJar(context.Background(), filepath.Dir(existing), target, "lithium")
	require.Error(t, err)
	body, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "a partial response must not replace the installed jar")
	require.NoFileExists(t, filepath.Join(filepath.Dir(existing), "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, filepath.Dir(existing))
}

func TestFabricOptimizationInstallPreservesJarOnTruncatedBody(t *testing.T) {
	existing, target := fabricOptimizationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
	}, "lithium-2.0.jar")

	err := installFabricOptimizationJar(context.Background(), filepath.Dir(existing), target, "lithium")
	require.Error(t, err)
	body, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "a truncated response must not replace the installed jar")
	require.NoFileExists(t, filepath.Join(filepath.Dir(existing), "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, filepath.Dir(existing))
}

func assertNoFabricOptimizationTemp(t *testing.T, modsDir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(modsDir, ".fabric-optimization-*.tmp"))
	require.NoError(t, err)
	require.Empty(t, matches, "a failed download must not leave a partial temporary jar")
}
