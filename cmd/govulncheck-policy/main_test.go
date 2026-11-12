// SPDX-License-Identifier: MPL-2.0
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const configJSON = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scanner_version":"v1.9.0","scan_mode":"source","scan_level":"symbol"}}` + "\n"
const sbomJSON = `{"SBOM":{"roots":["example.com/app"]}}` + "\n"

func TestParsePolicy(t *testing.T) {
	called := `{"finding":{"osv":"GO-TEST","trace":[{"module":"example.com/dependency","function":"Unsafe"}]}}`
	fixed := strings.Replace(called, `"osv":"GO-TEST"`, `"osv":"GO-TEST","fixed_version":"v1.2.3"`, 1)
	for _, tc := range []struct {
		name, json, outcome string
		wantError           bool
	}{
		{"clean", configJSON + sbomJSON, "clean", false},
		{"unfixed", configJSON + sbomJSON + called, "unfixed", false},
		{"fixable", configJSON + sbomJSON + fixed, "fixable", false},
		{"duplicate traces", configJSON + sbomJSON + called + called, "unfixed", false},
		{"only module required", configJSON + sbomJSON + `{"finding":{"osv":"GO-TEST","fixed_version":"v1.2.3","trace":[{"module":"dep"}]}}`, "clean", false},
		{"empty", "", "", true},
		{"config only", configJSON, "", true},
		{"truncated", configJSON + sbomJSON + `{"finding":`, "", true},
		{"malformed", configJSON + sbomJSON + "network failure", "", true},
		{"missing identity", configJSON + sbomJSON + `{"finding":{}}`, "", true},
		{"null", "null", "", true},
		{"null config", `{"config":null}`, "", true},
		{"duplicate config", configJSON + configJSON + sbomJSON, "", true},
		{"wrong protocol", strings.Replace(configJSON, "v1.0.0", "v2.0.0", 1) + sbomJSON, "", true},
		{"wrong scanner", strings.Replace(configJSON, "v1.9.0", "v9.0.0", 1) + sbomJSON, "", true},
		{"weaker scan", strings.Replace(configJSON, "symbol", "module", 1) + sbomJSON, "", true},
		{"missing roots", configJSON + `{"SBOM":{}}`, "", true},
		{"unknown message", configJSON + sbomJSON + `{"failure":{"message":"oops"}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parse(strings.NewReader(tc.json))
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
			if !tc.wantError && r.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", r.Outcome, tc.outcome)
			}
			if tc.name == "duplicate traces" && len(r.Unfixed) != 1 {
				t.Fatalf("duplicate findings not coalesced: %+v", r)
			}
		})
	}
}

// Exercise the actual subprocess seam so even valid-looking output cannot hide
// scanner failures. The child test executable never binds a network socket.
func TestScannerProcess(t *testing.T) {
	for _, mode := range []string{"clean", "typecheck", "network", "partial", "text-findings-exit"} {
		t.Run(mode, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "report-*.json")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			cmd := exec.Command(os.Args[0], "-test.run=^TestScannerHelper$")
			cmd.Env = append(os.Environ(), "CARBONPANEL_SCANNER_TEST="+mode)
			r, err := run(cmd, f, io.Discard)
			if mode == "clean" {
				if err != nil || r.Outcome != "clean" {
					t.Fatalf("clean scan: %+v, %v", r, err)
				}
			} else if err == nil {
				t.Fatalf("%s scanner failure passed: %+v", mode, r)
			}
		})
	}
	t.Run("missing executable", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "missing-*.json")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		if _, err := run(exec.Command(t.TempDir()+"/missing-scanner"), f, io.Discard); err == nil {
			t.Fatal("missing scanner passed")
		}
	})
}

func TestScannerHelper(t *testing.T) {
	mode := os.Getenv("CARBONPANEL_SCANNER_TEST")
	if mode == "" {
		return
	}
	fmt.Print(configJSON)
	if mode == "partial" {
		os.Exit(0)
	}
	fmt.Print(sbomJSON)
	switch mode {
	case "typecheck", "network":
		fmt.Fprintln(os.Stderr, mode+" failed")
		os.Exit(1)
	case "text-findings-exit":
		os.Exit(3)
	}
	os.Exit(0)
}
