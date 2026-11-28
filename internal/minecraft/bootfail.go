// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"fmt"
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

// logEntry is one timestamped log line inside the current-boot window.
type logEntry struct {
	when time.Time
	text string
}

// DetectBootFailure scans timestamped Docker log lines for a fatal
// game-process failure in the current boot, returning a human-readable
// reason. Lines carry Docker timestamps ("2006-01-02T15:04:05.999Z message").
// Only lines at or after `since` (the container start) are evidence, and only
// lines after the last boot marker within that window. Lines without a
// parseable timestamp are ignored: without one there is no evidence the
// failure belongs to this boot, and attributing a previous attempt's crash to
// a healthy boot would paint a starting server red.
func DetectBootFailure(lines []string, since time.Time) (string, bool) {
	var window []logEntry
	for _, line := range lines {
		text, when, ok := splitDockerTimestamp(line)
		if !ok {
			continue
		}
		if !since.IsZero() && when.Before(since) {
			continue
		}
		window = append(window, logEntry{when: when, text: text})
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
	// No single fatal line, but the same stack trace repeating within one
	// boot is a crash loop the entrypoint is retrying through.
	if reason, ok := repeatedStackReason(window[start:]); ok {
		return reason, true
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

// repeatedStackThreshold is how many identical exception headers within one
// boot mark a crash loop rather than a mod's one-off warning. The threshold
// is deliberately high: a false positive here paints a healthy boot red.
const repeatedStackThreshold = 3

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
	// A JVM that cannot allocate is dead in practice: the entrypoint retries
	// into the same heap limit.
	if strings.Contains(text, "java.lang.OutOfMemoryError") {
		return "Minecraft server ran out of memory (java.lang.OutOfMemoryError)", true
	}
	// The tick-loop watchdog crash title (vanilla/Paper crash reports echo it
	// into the log): the server thread died and the process is going down.
	if strings.Contains(text, "Exception in server tick loop") {
		return "Minecraft server crashed in its server tick loop (watchdog)", true
	}
	if strings.Contains(text, " requires ") && strings.Contains(text, "which is missing!") {
		return truncateReason(cleanReason(text)), true
	}
	if strings.Contains(text, "Failed to start the minecraft server") {
		return "Minecraft server failed to start (see console logs)", true
	}
	return "", false
}

// repeatedStackReason reports the same Java stack repeating within the
// current boot window (a crash loop one boot attempt is retrying through).
// Stacks are deduplicated by their first line — the exception header — and
// only count when an actual stack frame follows, so alarming one-off prose
// from mods is not evidence.
func repeatedStackReason(window []logEntry) (string, bool) {
	counts := make(map[string]int)
	for i, e := range window {
		header, ok := exceptionHeader(e.text)
		if !ok || !hasStackFrame(window, i) {
			continue
		}
		counts[header]++
	}
	best := ""
	bestCount := 0
	for header, count := range counts {
		if count >= repeatedStackThreshold && (count > bestCount || (count == bestCount && header < best)) {
			best, bestCount = header, count
		}
	}
	if bestCount == 0 {
		return "", false
	}
	return truncateReason(fmt.Sprintf("repeated identical crash stack (%dx): %s", bestCount, best)), true
}

// exceptionHeader reports whether a log line opens a Java stack trace and
// returns the dedup key (the stripped header line). Mod prose that merely
// contains the word "exception" in lowercase does not match; a Java
// exception header does.
func exceptionHeader(text string) (string, bool) {
	cleaned := cleanReason(text)
	if cleaned == "" || len(cleaned) > 300 {
		return "", false
	}
	if !strings.Contains(cleaned, "Exception") && !strings.Contains(cleaned, "Error") {
		return "", false
	}
	if isStackFrame(cleaned) {
		return "", false
	}
	return cleaned, true
}

// isStackFrame reports whether a log line is a Java stack frame
// ("at pkg.Class.method(Class.java:12)").
func isStackFrame(text string) bool {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "at ") {
		return true
	}
	return strings.Contains(trimmed, ".java:")
}

// hasStackFrame reports whether a stack frame follows the header within a
// few lines, anchoring the header to an actual stack trace.
func hasStackFrame(window []logEntry, headerIndex int) bool {
	for i := headerIndex + 1; i < len(window) && i <= headerIndex+3; i++ {
		if isStackFrame(cleanReason(window[i].text)) {
			return true
		}
	}
	return false
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
