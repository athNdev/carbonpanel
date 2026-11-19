// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"encoding/json"
	"strings"
	"testing"
)

// These fixtures encode the three defects found by booting the EXACT shipped
// units with a real engine on a clean systemd fixture. They are contract
// assertions, not a claim that the unit boots; the real boot evidence lives in
// the external qualification report.

func TestPrivatePidfileRenderedInConfigAndUnit(t *testing.T) {
	for _, d := range []DaemonSpec{privateSpec(), rootfulSpec()} {
		m, err := d.BuildManifest()
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal([]byte(m.ConfigJSON), &cfg); err != nil {
			t.Fatal(err)
		}
		got, ok := cfg["pidfile"].(string)
		if !ok || got != d.Pidfile {
			t.Fatalf("config pidfile = %v, want %q", cfg["pidfile"], d.Pidfile)
		}
		if strings.Contains(m.ConfigJSON, "/var/run/docker.pid") {
			t.Fatal("shared default pidfile leaked into config")
		}
		if d.Mode == ModeRootful && !strings.Contains(m.Unit, "PIDFile="+d.Pidfile) {
			t.Fatalf("rootful unit missing private PIDFile directive: %s", m.Unit)
		}
		for _, c := range CheckOptionParity(m.ConfigJSON, m.Unit) {
			if !c.Passed {
				t.Fatalf("parity failed: %s: %s", c.Code, c.Message)
			}
		}
	}
}

func TestRootfulUnitHasDefaultContainerCapabilities(t *testing.T) {
	d := rootfulSpec()
	unit, err := d.RenderUnit()
	if err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"CAP_FSETID", "CAP_KILL", "CAP_NET_BIND_SERVICE", "CAP_SETFCAP"} {
		if !strings.Contains(unit, cap) {
			t.Fatalf("rootful unit omits required default-cap %s", cap)
		}
		// Must be in both directives, not just one.
		for _, directive := range []string{"CapabilityBoundingSet=", "AmbientCapabilities="} {
			line := ""
			for _, l := range strings.Split(unit, "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), directive) {
					line = l
					break
				}
			}
			if !strings.Contains(line, cap) {
				t.Fatalf("%s missing %s: %q", directive, cap, line)
			}
		}
	}
	// Hardening that must survive the capability fix.
	for _, keep := range []string{"NoNewPrivileges=no", "ProtectSystem=full", "Delegate=cpu cpuset io memory pids"} {
		if !strings.Contains(unit, keep) {
			t.Fatalf("rootful unit lost hardening %q", keep)
		}
	}
	for _, c := range CheckUnitContract(unit, ModeRootful) {
		if !c.Passed {
			t.Fatalf("rootful unit contract failed: %s: %s", c.Code, c.Message)
		}
	}
}

func TestRootlessUnitNotifyAccess(t *testing.T) {
	d := privateSpec()
	unit, err := d.RenderUnit()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, "Type=notify") {
		t.Fatal("rootless unit must remain Type=notify")
	}
	if !strings.Contains(unit, "NotifyAccess=all") {
		t.Fatalf("rootless unit missing NotifyAccess=all: %s", unit)
	}
	for _, c := range CheckUnitContract(unit, ModeRootless) {
		if !c.Passed {
			t.Fatalf("rootless unit contract failed: %s: %s", c.Code, c.Message)
		}
	}
}

func TestSpecFromFactsDerivesPrivatePidfile(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false // rootful selection
	sel := ApplyGates(f)
	if sel.Mode != ModeRootful {
		t.Fatalf("expected rootful, got %q", sel.Mode)
	}
	d := SpecFromFacts("node-fixture", f, sel)
	want := "/run/user/1000/carbon-node/docker.pid"
	if d.Pidfile != want {
		t.Fatalf("derived pidfile = %q, want %q", d.Pidfile, want)
	}
	if code := d.ValidateOrRefuse(); code != "" {
		t.Fatalf("derived spec refused: %s", code)
	}
}
