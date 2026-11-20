// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"time"

	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Protocol range for this mode. It is advertised through the shared
// NegotiateVersion contract; a controller outside the range produces an
// explicit UPGRADE_REQUIRED outcome rather than a silent downgrade.
const (
	ProtocolVersion     int32 = enroll.ProtocolVersion
	MinSupportedVersion int32 = 1
	MaxSupportedVersion int32 = 2
	AgentVersion              = "carbon-node-native/0.1.0"
)

// PreflightReport converts a gate selection into the shared, redacted
// NodePreflightReport wire type. No host path, key, secret or real endpoint is
// emitted: checks carry only stable codes, messages and remedies.
func PreflightReport(nodeID string, sel Selection, observedAt time.Time) *v1.NodePreflightReport {
	return preflightReportAt(nodeID, sel, timestamppb.New(observedAt))
}

func preflightReportAt(nodeID string, sel Selection, ts *timestamppb.Timestamp) *v1.NodePreflightReport {
	status := v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_PASSED
	if sel.Mode == ModeNone {
		status = v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_FAILED
	}
	report := &v1.NodePreflightReport{
		NodeId:     nodeID,
		Status:     status,
		ObservedAt: ts,
	}
	for _, c := range sel.Checks {
		cs := v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_PASSED
		if c.Unknown {
			cs = v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_UNKNOWN
		} else if !c.Passed {
			cs = v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_FAILED
		}
		report.Checks = append(report.Checks, &v1.NodePreflightCheck{
			Code:    c.Code,
			Status:  cs,
			Message: c.Message,
			Remedy:  c.Remedy,
		})
	}
	return report
}

// Capabilities converts qualified facts and the selected mode into the shared
// capability snapshot. NodeCapability is a USABLE feature grant: the controller
// gates operations on it, so a grant is withheld unless a captive daemon mode
// actually qualified (sel.Mode != ModeNone). This is a single boundary guard for
// every daemon-dependent feature; no individual mount condition is special.
// Host diagnostics (cgroup_version, storage_driver, systemd, distro, ...) stay
// in the snapshot even when no mode qualifies, so a fail-closed host is still
// inspectable. Agent facilities that work independently of a daemon mode
// (storage worker, drain) remain granted.
func Capabilities(f HostFacts, sel Selection) *v1.NodeCapabilities {
	caps := make([]v1.NodeCapability, 0, 8)
	qualified := sel.Mode != ModeNone
	if qualified {
		// Isolation grants additionally require their own mount/path to be
		// accepted. A collision refuses that grant without weakening others in
		// a qualified mode.
		if !hasFailedCode(sel.Checks, CodePrivateSocketCollision) {
			caps = append(caps, v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET)
		}
		if !hasFailedCode(sel.Checks, CodeDedicatedRootCollision) {
			caps = append(caps, v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS)
		}
		if !hasFailedCode(sel.Checks, CodeNamespaceCollision) {
			caps = append(caps, v1.NodeCapability_NODE_CAPABILITY_CONTAINERD_NAMESPACE)
		}
	}
	// Independently usable agent facilities are retained on a fail-closed host.
	caps = append(caps,
		v1.NodeCapability_NODE_CAPABILITY_STORAGE_WORKER,
		v1.NodeCapability_NODE_CAPABILITY_DRAIN,
	)
	if qualified {
		if sel.Mode == ModeRootless {
			caps = append(caps, v1.NodeCapability_NODE_CAPABILITY_ROOTLESS)
		}
		if f.CgroupVersion == "v2" && hasAll(f.DelegatedControllers, "cpu", "cpuset", "io", "memory", "pids") {
			caps = append(caps, v1.NodeCapability_NODE_CAPABILITY_CGROUP_V2)
		}
		if f.PrivilegedPortStart <= 1024 {
			caps = append(caps, v1.NodeCapability_NODE_CAPABILITY_PRIVILEGED_PORT)
		}
	}
	return &v1.NodeCapabilities{
		ProtocolVersion:     ProtocolVersion,
		MinSupportedVersion: MinSupportedVersion,
		MaxSupportedVersion: MaxSupportedVersion,
		AgentVersion:        AgentVersion,
		EngineVersion:       "pinned-by-installer",
		Distro:              f.Distro,
		Architecture:        f.Architecture,
		KernelVersion:       f.KernelVersion,
		CgroupVersion:       f.CgroupVersion,
		StorageDriver:       f.StorageDriver,
		Systemd:             f.Systemd,
		Capabilities:        caps,
	}
}

func hasFailedCode(checks []Check, code string) bool {
	for _, c := range checks {
		if c.Code == code && !c.Passed && !c.Unknown {
			return true
		}
	}
	return false
}

// NegotiateVersion implements the shared version-skew outcome for this mode.
func NegotiateVersion(endpoint *v1.VersionNegotiation) *v1.NegotiateVersionResponse {
	resp := &v1.NegotiateVersionResponse{}
	if endpoint == nil {
		resp.Reason = "UPGRADE_REQUIRED: missing endpoint negotiation"
		return resp
	}
	low := maxInt32(MinSupportedVersion, endpoint.MinSupportedVersion)
	high := minInt32(MaxSupportedVersion, endpoint.MaxSupportedVersion)
	if low > high {
		resp.Reason = "UPGRADE_REQUIRED: no mutually supported protocol version"
		return resp
	}
	resp.Supported = true
	resp.SelectedVersion = high
	return resp
}

func minInt32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
