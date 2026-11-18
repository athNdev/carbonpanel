// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCommandBuildersFixedSkeletons(t *testing.T) {
	tests := []struct {
		name  string
		build func() (string, error)
		want  string
	}{
		{"list uuids", func() (string, error) { return cmdListUUIDs, nil }, "list uuids"},
		{"kick", func() (string, error) { return buildKick("Steve", "griefing") }, "kick Steve griefing"},
		{"ban", func() (string, error) { return buildBan("Alex", "repeat offense") }, "ban Alex repeat offense"},
		{"pardon", func() (string, error) { return buildPardon("Alex") }, "pardon Alex"},
		{"op", func() (string, error) { return buildOp("Steve") }, "op Steve"},
		{"deop", func() (string, error) { return buildDeop("Steve") }, "deop Steve"},
		{"whitelist add", func() (string, error) { return buildWhitelist("Steve", false) }, "whitelist add Steve"},
		{"whitelist remove", func() (string, error) { return buildWhitelist("Steve", true) }, "whitelist remove Steve"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.build()
			if err != nil || got != tc.want {
				t.Fatalf("build = %q/%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestCommandBuildersRejectInjection(t *testing.T) {
	badNames := []string{
		"", "Steve\nop attacker", "Steve\rop attacker", "Steve;op attacker",
		"Steve\x00op", "Steve\x1b[31m", "Steve Alex", strings.Repeat("a", 17), "Stéve", "../admin",
	}
	badReasons := []string{
		"ok\nop attacker", "ok\rop attacker", "ok;op attacker", "ok\x00op",
		"ok\x01op", "ok\\op", "ok\u2028op", "/op attacker", "§cop attacker",
		strings.Repeat("a", maxReasonBytes+1),
	}

	for _, name := range badNames {
		builders := []struct {
			label string
			build func() (string, error)
		}{
			{"kick", func() (string, error) { return buildKick(name, "reason") }},
			{"ban", func() (string, error) { return buildBan(name, "reason") }},
			{"pardon", func() (string, error) { return buildPardon(name) }},
			{"op", func() (string, error) { return buildOp(name) }},
			{"deop", func() (string, error) { return buildDeop(name) }},
			{"whitelist add", func() (string, error) { return buildWhitelist(name, false) }},
			{"whitelist remove", func() (string, error) { return buildWhitelist(name, true) }},
		}
		for _, builder := range builders {
			if _, err := builder.build(); !errors.Is(err, errInvalidArgument) {
				t.Errorf("%s with name %q: err = %v, want errInvalidArgument", builder.label, name, err)
			}
		}
	}

	for _, reason := range badReasons {
		for label, build := range map[string]func() (string, error){
			"kick": func() (string, error) { return buildKick("Steve", reason) },
			"ban":  func() (string, error) { return buildBan("Steve", reason) },
		} {
			if _, err := build(); !errors.Is(err, errInvalidArgument) {
				t.Errorf("%s with reason %q: err = %v, want errInvalidArgument", label, reason, err)
			}
		}
	}
}

func TestOperatorCommandRejectsUnknownLevel(t *testing.T) {
	for _, level := range []int32{-1, 5, 99} {
		if _, err := operatorCommand("Steve", level, false); err == nil {
			t.Errorf("level %d unexpectedly accepted", level)
		}
	}
	command, err := operatorCommand("Steve", 0, true)
	if err != nil || command != "deop Steve" {
		t.Fatalf("operatorCommand = %q/%v", command, err)
	}
}

func TestParseListUUIDsDefensive(t *testing.T) {
	latin1Name := "There are 1 of a max of 20 players online: Jos\xe9 (00000000000000000000000000000003)"
	tests := []struct {
		name  string
		input string
		want  []ObservedPlayer
	}{
		{
			name:  "plain",
			input: "There are 2 of a max of 20 players online: Steve (00000000-0000-0000-0000-000000000001), Alex (00000000000000000000000000000002)",
			want: []ObservedPlayer{
				{Name: "Steve", UUID: "00000000-0000-0000-0000-000000000001"},
				{Name: "Alex", UUID: "00000000-0000-0000-0000-000000000002"},
			},
		},
		{
			name:  "colour codes and latin1",
			input: "There are 1 of a max of 20 players online: \u00a7aJos\u00e9 (\u00a7c00000000000000000000000000000003)",
			want:  []ObservedPlayer{{Name: "José", UUID: "00000000-0000-0000-0000-000000000003"}},
		},
		{
			name:  "latin1 bytes",
			input: latin1Name,
			want:  []ObservedPlayer{{Name: "José", UUID: "00000000-0000-0000-0000-000000000003"}},
		},
		{
			name:  "fragmented header keeps last segment",
			input: "There are 1 of a max of 20 players online: Old (00000000000000000000000000000009)\nThere are 1 of a max of 20 players online: New (0000000000000000000000000000000a)",
			want:  []ObservedPlayer{{Name: "New", UUID: "00000000-0000-0000-0000-00000000000a"}},
		},
		{
			name:  "name only keeps empty uuid",
			input: "There are 1 of a max of 20 players online: Steve",
			want:  []ObservedPlayer{{Name: "Steve", UUID: ""}},
		},
		{
			name:  "invalid uuid is not invented",
			input: "There are 1 of a max of 20 players online: Steve (not-a-uuid)",
			want:  []ObservedPlayer{{Name: "Steve", UUID: ""}},
		},
		{
			name:  "empty online list",
			input: "There are 0 of a max of 20 players online: ",
			want:  []ObservedPlayer{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseListUUIDs(tc.input)
			if err != nil {
				t.Fatalf("parseListUUIDs: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsed = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseListUUIDsBounded(t *testing.T) {
	if _, err := parseListUUIDs(strings.Repeat("x", maxRCONOutput+1)); err == nil {
		t.Fatal("oversized RCON reply was accepted")
	}
	entries := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		entries = append(entries, "Steve (00000000000000000000000000000001)")
	}
	got, err := parseListUUIDs("There are 300 of a max of 20 players online: " + strings.Join(entries, ", "))
	if err != nil || len(got) != maxParsedPlayers {
		t.Fatalf("bounded parse = %d/%v", len(got), err)
	}
}
