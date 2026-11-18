// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Command skeletons are fixed constants. Caller data is only ever inserted
// into a validated name or reason slot; no caller can supply a verb, flag or
// extra argument. Fixtures depend on these exact strings.
const (
	cmdListUUIDs     = "list uuids"
	cmdKick          = "kick"
	cmdBan           = "ban"
	cmdPardon        = "pardon"
	cmdOp            = "op"
	cmdDeop          = "deop"
	cmdWhitelist     = "whitelist"
	cmdWhitelistAdd  = "add"
	cmdWhitelistRem  = "remove"
	maxNameBytes     = 16  // Java player names are 1-16 ASCII characters
	maxReasonBytes   = 256 // bounded reason for kick/ban
	maxCommandBytes  = 512 // bounded total command length
	maxParsedPlayers = 256 // bounded roster from RCON text
	maxRCONOutput    = 1 << 20
)

var errInvalidArgument = errors.New("playeradapters: invalid command argument")

func validName(name string) error {
	if name == "" || len(name) > maxNameBytes {
		return fmt.Errorf("%w: player name must be 1-%d characters", errInvalidArgument, maxNameBytes)
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return fmt.Errorf("%w: player name contains a disallowed character", errInvalidArgument)
	}
	return nil
}

// validReason accepts printable text but rejects every character that could
// terminate, extend or inject another command: controls (including NUL, LF,
// CR), semicolons, backslashes, colour-code markers, command prefixes and
// Unicode line separators. Length is bounded in bytes and runes.
func validReason(reason string) error {
	if len(reason) > maxReasonBytes {
		return fmt.Errorf("%w: reason exceeds %d bytes", errInvalidArgument, maxReasonBytes)
	}
	if strings.HasPrefix(reason, "/") {
		return fmt.Errorf("%w: reason must not start with a command prefix", errInvalidArgument)
	}
	for _, r := range reason {
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) || r == ';' || r == '\\' || r == '§' {
			return fmt.Errorf("%w: reason contains a disallowed character", errInvalidArgument)
		}
	}
	return nil
}

func finishCommand(parts ...string) (string, error) {
	command := strings.Join(parts, " ")
	if len(command) > maxCommandBytes {
		return "", fmt.Errorf("%w: command exceeds %d bytes", errInvalidArgument, maxCommandBytes)
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return "", fmt.Errorf("%w: command contains a control character", errInvalidArgument)
	}
	return command, nil
}

func buildKick(name, reason string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	if err := validReason(reason); err != nil {
		return "", err
	}
	return finishCommand(cmdKick, name, reason)
}

func buildBan(name, reason string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	if err := validReason(reason); err != nil {
		return "", err
	}
	return finishCommand(cmdBan, name, reason)
}

func buildPardon(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	return finishCommand(cmdPardon, name)
}

func buildOp(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	return finishCommand(cmdOp, name)
}

func buildDeop(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	return finishCommand(cmdDeop, name)
}

func buildWhitelist(name string, remove bool) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	action := cmdWhitelistAdd
	if remove {
		action = cmdWhitelistRem
	}
	return finishCommand(cmdWhitelist, action, name)
}

// parseListUUIDs defensively parses the text reply to "list uuids". RCON
// replies may be fragmented, Latin-1 encoded and decorated with Minecraft
// colour codes. A reply that does not contain a UUID is kept as a name-only
// observation: a UUID is never guessed from the name.
func parseListUUIDs(raw string) ([]ObservedPlayer, error) {
	if len(raw) > maxRCONOutput {
		return nil, fmt.Errorf("playeradapters: RCON reply exceeds %d bytes", maxRCONOutput)
	}
	text := stripMinecraftCodes(normalizeLatin1(raw))
	lower := strings.ToLower(text)
	body := text
	if idx := strings.LastIndex(lower, "players online:"); idx >= 0 {
		body = text[idx+len("players online:"):]
	}

	players := make([]ObservedPlayer, 0)
	for _, field := range strings.Split(body, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		name, uuid := splitPlayerField(field)
		if name == "" {
			continue
		}
		players = append(players, ObservedPlayer{Name: name, UUID: uuid})
		if len(players) >= maxParsedPlayers {
			break
		}
	}
	return players, nil
}

func splitPlayerField(field string) (name, uuid string) {
	namePart := field
	open := strings.LastIndexByte(field, '(')
	if open > 0 && strings.HasSuffix(field, ")") {
		candidate := field[open+1 : len(field)-1]
		if canonical, ok := canonicalUUID(candidate); ok {
			uuid = canonical
		}
		namePart = field[:open]
	}
	name = strings.TrimSpace(namePart)
	name = strings.Trim(name, "[]{} ")
	if name == "" || len(name) > 36 {
		return "", ""
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", ""
		}
	}
	return name, uuid
}

func canonicalUUID(s string) (string, bool) {
	compact := strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(compact) != 32 {
		return "", false
	}
	for _, r := range compact {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return "", false
		}
	}
	compact = strings.ToLower(compact)
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32], true
}

// normalizeLatin1 converts invalid UTF-8 bytes (typically Latin-1 name bytes
// from an RCON reply) to their Unicode code points while preserving valid
// UTF-8 sequences.
func normalizeLatin1(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(rune(s[0]))
			s = s[1:]
			continue
		}
		b.WriteRune(r)
		s = s[size:]
	}
	return b.String()
}

// stripMinecraftCodes removes both section-sign colour codes and ANSI escape
// sequences from an RCON reply.
func stripMinecraftCodes(s string) string {
	// Work on runes: section sign is two bytes in UTF-8, and byte-wise removal
	// would leave an invalid leading byte in the parsed name.
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case 0x1b: // ANSI escape sequence, terminated by 'm'
			for i < len(runes) && runes[i] != 'm' {
				i++
			}
			continue
		case '§':
			i++ // one colour/format character follows
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}
