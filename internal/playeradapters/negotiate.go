// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// minManagementJavaVersion is the first Java release with the native Server
// Management Protocol (25w35a / 1.21.9). D-MGMT gates real-server validation.
const minManagementJavaVersion = "1.21.9"

const decisionPending = "capability not verified / decision pending"

// negotiationRule is one row of the availability table evaluated by
// Negotiate. Availability is derived only from caller-supplied configuration;
// no source is probed here so negotiation stays deterministic and offline.
type negotiationRule struct {
	source     string
	available  bool
	reason     string
	detail     string
	newAdapter func() Adapter
}

// Negotiate resolves the highest-priority usable source for info. It performs
// no network I/O and never falls back to controller-local state. A typed
// ErrNoAdapter is returned when no row is available; the CapabilitySet is
// always populated, including fail-closed unsupported reasons.
func Negotiate(ctx context.Context, info ServerInfo, exec ExecFunc) (Adapter, CapabilitySet, error) {
	if err := ctx.Err(); err != nil {
		return nil, CapabilitySet{}, err
	}

	managementOK, managementReason := managementAvailable(info)
	rconOK, rconReason := rconAvailable(info)
	execOK, execReason := execAvailable(info, exec)

	rules := []negotiationRule{
		{
			source:     SourceManagementProtocol,
			available:  managementOK,
			reason:     managementReason,
			detail:     "Structured management protocol is configured; reachability and authentication are verified on first use. Real 1.21.9+ interoperability is gated by D-MGMT.",
			newAdapter: func() Adapter { return newManagementAdapter(info) },
		},
		{
			source:    SourceRCON,
			available: rconOK,
			reason:    rconReason,
			detail:    "Java RCON is enabled; replies are text and may be ambiguous, so observations are source-labelled and validated.",
			newAdapter: func() Adapter {
				return &rconAdapter{
					info:    info,
					send:    rconSend,
					timeout: rconCallTimeout,
				}
			},
		},
		{
			source:    SourceExec,
			available: execOK,
			reason:    execReason,
			detail:    "RCON is unavailable; using the caller-supplied node-scoped exec with fixed allowlisted commands.",
			newAdapter: func() Adapter {
				return &execAdapter{exec: exec, info: info, timeout: execCallTimeout}
			},
		},
	}

	caps := CapabilitySet{
		Management: managementOK,
		RCON:       rconOK,
		Exec:       execOK,
	}
	for _, rule := range rules {
		if !rule.available {
			caps.Unsupported = append(caps.Unsupported, UnsupportedCapability{
				Name: rule.source, Reason: rule.reason,
			})
		}
	}
	caps.Unsupported = append(caps.Unsupported, decisionGateCapabilities(info)...)

	for _, rule := range rules {
		if !rule.available {
			continue
		}
		caps.ChosenSource = rule.source
		caps.Detail = info.redact(rule.detail)
		return rule.newAdapter(), caps, nil
	}

	reasons := make([]string, 0, len(caps.Unsupported))
	for _, gap := range caps.Unsupported {
		reasons = append(reasons, gap.Name+": "+gap.Reason)
	}
	caps.Detail = info.redact("No source is usable for this server; " + strings.Join(reasons, "; ") + ". No controller-local fallback is used.")
	return nil, caps, fmt.Errorf("%w: %s", ErrNoAdapter, caps.Detail)
}

// decisionGateCapabilities exposes unresolved capabilities explicitly instead
// of implying them from vanilla sources. These are not implementation promises.
func decisionGateCapabilities(info ServerInfo) []UnsupportedCapability {
	gates := []UnsupportedCapability{
		{Name: "bridge.vitals", Reason: decisionPending},
		{Name: "bridge.inventory", Reason: decisionPending},
		{Name: "bridge.groups", Reason: decisionPending},
		{Name: "bridge.mute", Reason: decisionPending},
	}
	if strings.EqualFold(strings.TrimSpace(info.Edition), "java") {
		gates = append(gates, UnsupportedCapability{Name: "bedrock.parity", Reason: decisionPending})
	}
	return gates
}

func managementAvailable(info ServerInfo) (bool, string) {
	if !strings.EqualFold(strings.TrimSpace(info.Edition), "java") {
		return false, "the management protocol is a Java Edition feature; the edition is bedrock/unknown and Bedrock parity is " + decisionPending
	}
	if !info.ManagementEnabled {
		return false, "management-server-enabled is false; enable the management protocol or use the Java RCON/exec fallback"
	}
	if info.ManagementURL == "" {
		return false, "no controller-reachable management endpoint is configured"
	}
	parsed, err := url.Parse(info.ManagementURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return false, "management endpoint must be a ws:// or wss:// URL with a host"
	}
	if parsed.User != nil {
		return false, "management endpoint must not embed credentials in its URL"
	}
	if info.ManagementSecret == "" {
		return false, "no management bearer secret is configured; set one or use the Java RCON/exec fallback"
	}
	ok, err := versionAtLeast(info.MCVersion, minManagementJavaVersion)
	if err != nil || !ok {
		return false, fmt.Sprintf("Java %q is below the minimum %s required by the management protocol", info.MCVersion, minManagementJavaVersion)
	}
	return true, ""
}

func rconAvailable(info ServerInfo) (bool, string) {
	if !strings.EqualFold(strings.TrimSpace(info.Edition), "java") {
		return false, "RCON is a Java Edition feature; Bedrock XUID/allowlist flows are " + decisionPending
	}
	if !info.RCONEnabled {
		return false, "RCON is disabled for this server"
	}
	if info.RCONHost == "" || info.RCONPort <= 0 {
		return false, "RCON host or port is missing"
	}
	if info.RCONPassword == "" {
		return false, "RCON password is missing"
	}
	return true, ""
}

func execAvailable(info ServerInfo, exec ExecFunc) (bool, string) {
	if exec == nil {
		return false, "no caller-supplied node-scoped exec function was provided"
	}
	if !strings.EqualFold(strings.TrimSpace(info.Edition), "java") {
		return false, "node exec uses the Java rcon-cli path; Bedrock parity is " + decisionPending
	}
	return true, ""
}

// versionAtLeast compares dotted numeric prefixes such as "1.21.9",
// "1.21.9-rc1" or "1.22". A missing component is zero; an unparseable version
// is rejected so an unknown version never unlocks a newer protocol.
func versionAtLeast(got, minimum string) (bool, error) {
	g, err := versionParts(got)
	if err != nil {
		return false, err
	}
	m, err := versionParts(minimum)
	if err != nil {
		return false, err
	}
	for len(g) < len(m) {
		g = append(g, 0)
	}
	for len(m) < len(g) {
		m = append(m, 0)
	}
	for i := range g {
		if g[i] > m[i] {
			return true, nil
		}
		if g[i] < m[i] {
			return false, nil
		}
	}
	return true, nil
}

func versionParts(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("empty version")
	}
	raw := strings.Split(v, ".")
	parts := make([]int, 0, len(raw))
	for _, segment := range raw {
		i := 0
		for i < len(segment) && segment[i] >= '0' && segment[i] <= '9' {
			i++
		}
		if i == 0 {
			return nil, fmt.Errorf("non-numeric version component %q", segment)
		}
		n, err := strconv.Atoi(segment[:i])
		if err != nil {
			return nil, fmt.Errorf("invalid version component %q: %w", segment, err)
		}
		parts = append(parts, n)
	}
	return parts, nil
}
