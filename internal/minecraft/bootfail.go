// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"regexp"
	"strings"
	"time"
)

// bootAttemptMarkers are log substrings that open a fresh game boot attempt.
// The container can outlive a crashed JVM (the entrypoint retries inside a
// live container), so a fatal line only belongs to the current boot when it
// comes after the last marker.
var bootAttemptMarkers = []string{
	"Starting the Minecraft server", // itzg entrypoint
	"Loading Minecraft ",            // Fabric loader
	"Starting minecraft server",     // vanilla / Paper
	"Starting Minecraft server",     // Forge / NeoForge
}

// gameLogPrefix strips the "[thread/level]:" prefix the game adds, so a
// reason reads as a sentence instead of log syntax.
var gameLogPrefix = regexp.MustCompile(`^\[[^\]]*\]:\s*`)

// runnerExitCode matches itzg's machine-readable failure report, e.g.
// `WARN mc-server-runner Minecraft server failed. ... {"exitCode": 1}`.
var runnerExitCode = regexp.MustCompile(`"exitCode":\s*"?(\d+)"?`)

// DetectBootFailure scans timestamped Docker log lines for a fatal
// game-process failure in the current boot, returning a human-readable
// reason. Lines carry Docker timestamps ("2006-01-02T15:04:05.999Z message").
// Only lines at or after `since` (the container start) are evidence, and only
// lines after the last boot marker within that window. Lines without a
// parseable timestamp are ignored: without one there is no evidence the
// failure belongs to this boot, and attributing a previous attempt's crash to
// a healthy boot would paint a starting server red.
func DetectBootFailure(lines []string, since time.Time) (string, bool) {
	type entry struct {
		when time.Time
		text string
	}
	var window []entry
	for _, line := range lines {
		text, when, ok := splitDockerTimestamp(line)
		if !ok {
			continue
		}
		if !since.IsZero() && when.Before(since) {
			continue
		}
		window = append(window, entry{when: when, text: text})
	}
	start := 0
	for i, e := range window {
		if isBootMarker(e.text) {
			start = i + 1
		}
	}
	for _, e := range window[start:] {
		if reason, ok := matchFatalLine(e.text); ok {
			return reason, true
		}
	}
	return "", false
}

// splitDockerTimestamp splits a "timestamp message" line from `docker logs
// --timestamps` (RFC3339Nano, always UTC from the daemon).
func splitDockerTimestamp(line string) (string, time.Time, bool) {
	head, tail, found := strings.Cut(line, " ")
	if !found {
		return "", time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(head))
	if err != nil {
		return "", time.Time{}, false
	}
	return tail, when, true
}

func isBootMarker(text string) bool {
	for _, m := range bootAttemptMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// matchFatalLine matches lines that unambiguously report a dead game process.
// Mods log alarming but non-fatal lines constantly (missing textures,
// unhealthyMixin configs, version warnings), so this list stays narrow on
// purpose: a false positive here paints a healthy boot red.
func matchFatalLine(text string) (string, bool) {
	if strings.Contains(text, "mc-server-runner") && strings.Contains(text, `"exitCode"`) &&
		!strings.Contains(text, `"exitCode": 0`) {
		if m := runnerExitCode.FindStringSubmatch(text); len(m) == 2 {
			return "Minecraft server process failed (exit code " + m[1] + ")", true
		}
		return "Minecraft server process failed", true
	}
	if strings.Contains(text, "which is missing!") {
		return truncateReason(cleanReason(text)), true
	}
	if strings.Contains(text, "Failed to start the minecraft server") {
		return "Minecraft server failed to start (see console logs)", true
	}
	return "", false
}

// cleanReason drops the game log prefix so the reason reads as a sentence.
func cleanReason(text string) string {
	return gameLogPrefix.ReplaceAllString(strings.TrimSpace(text), "")
}

func truncateReason(s string) string {
	const maxReasonRunes = 300
	r := []rune(s)
	if len(r) <= maxReasonRunes {
		return s
	}
	return string(r[:maxReasonRunes]) + "..."
}
