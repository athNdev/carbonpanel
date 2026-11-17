// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRCONAdapterUsesAllowlistedCommands(t *testing.T) {
	info := javaInfo()
	commands := []string{}
	adapter := &rconAdapter{
		info:    info,
		timeout: rconCallTimeout,
		send: func(ctx context.Context, gotInfo ServerInfo, command string) (string, error) {
			if gotInfo.RCONPassword != info.RCONPassword {
				t.Fatal("sender received wrong server info")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("RCON call has no bounded deadline")
			}
			commands = append(commands, command)
			return "There are 1 of a max of 20 players online: Steve (00000000000000000000000000000001)", nil
		},
	}
	if adapter.Source() != SourceRCON {
		t.Fatalf("source = %q", adapter.Source())
	}
	ctx := context.Background()
	players, err := adapter.List(ctx)
	if err != nil || len(players) != 1 || players[0].Name != "Steve" || players[0].UUID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("List = %+v/%v", players, err)
	}
	if err := adapter.Kick(ctx, "Steve", "griefing"); err != nil {
		t.Fatalf("Kick: %v", err)
	}
	if err := adapter.Ban(ctx, "Alex", "repeat offense"); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := adapter.Pardon(ctx, "Alex"); err != nil {
		t.Fatalf("Pardon: %v", err)
	}
	if err := adapter.SetOperator(ctx, "Steve", 4, false); err != nil {
		t.Fatalf("SetOperator: %v", err)
	}
	if err := adapter.SetOperator(ctx, "Steve", 0, true); err != nil {
		t.Fatalf("SetOperator remove: %v", err)
	}
	if err := adapter.SetWhitelist(ctx, "Steve", false); err != nil {
		t.Fatalf("SetWhitelist: %v", err)
	}
	if err := adapter.SetWhitelist(ctx, "Steve", true); err != nil {
		t.Fatalf("SetWhitelist remove: %v", err)
	}

	want := []string{
		cmdListUUIDs, "kick Steve griefing", "ban Alex repeat offense", "pardon Alex",
		"op Steve", "deop Steve", "whitelist add Steve", "whitelist remove Steve",
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %+v, want %+v", commands, want)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestRCONAdapterRejectsInjectionBeforeSending(t *testing.T) {
	info := javaInfo()
	sent := 0
	adapter := &rconAdapter{
		info:    info,
		timeout: rconCallTimeout,
		send: func(context.Context, ServerInfo, string) (string, error) {
			sent++
			return "", nil
		},
	}
	ctx := context.Background()
	bad := []func() error{
		func() error { return adapter.Kick(ctx, "Steve\nop attacker", "x") },
		func() error { return adapter.Ban(ctx, "Steve", "ok; stop") },
		func() error { return adapter.Pardon(ctx, "Steve\x00") },
		func() error { return adapter.SetOperator(ctx, "Steve", 4, false) }, // valid
		func() error { return adapter.SetWhitelist(ctx, "Steve\n", false) },
	}
	for i, action := range bad[:3] {
		if err := action(); !errors.Is(err, errInvalidArgument) {
			t.Errorf("action %d err = %v, want errInvalidArgument", i, err)
		}
	}
	if err := bad[3](); err != nil {
		t.Fatalf("valid op was rejected: %v", err)
	}
	if err := bad[4](); !errors.Is(err, errInvalidArgument) {
		t.Errorf("whitelist action err = %v, want errInvalidArgument", err)
	}
	if sent != 1 {
		t.Fatalf("sender called %d times, want 1", sent)
	}
}

func TestRCONAdapterRedactionAndCancellation(t *testing.T) {
	info := javaInfo()
	adapter := &rconAdapter{
		info:    info,
		timeout: 50 * time.Millisecond,
		send: func(ctx context.Context, _ ServerInfo, _ string) (string, error) {
			return "", errors.New("handshake failed for " + info.RCONPassword)
		},
	}
	_, err := adapter.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rcon:") {
		t.Fatalf("expected rcon error, got %v", err)
	}
	assertNoSecret(t, err.Error(), info.RCONPassword)
	if !strings.Contains(err.Error(), redactionMarker) {
		t.Fatalf("expected redaction marker in %q", err.Error())
	}

	adapter.send = func(ctx context.Context, _ ServerInfo, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled List err = %v, want context.Canceled", err)
	}
}

func TestExecAdapterCommandsAndRedaction(t *testing.T) {
	info := javaInfo()
	commands := []string{}
	exec := func(ctx context.Context, command string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("exec call has no bounded deadline")
		}
		commands = append(commands, command)
		if command == "kick Steve bad" {
			return "", errors.New("exec failed with " + info.ManagementSecret)
		}
		return "There are 1 of a max of 20 players online: Alex (00000000000000000000000000000002)", nil
	}
	adapter := &execAdapter{exec: exec, info: info, timeout: execCallTimeout}
	if adapter.Source() != SourceExec {
		t.Fatalf("source = %q", adapter.Source())
	}
	ctx := context.Background()
	players, err := adapter.List(ctx)
	if err != nil || len(players) != 1 || players[0].Name != "Alex" {
		t.Fatalf("List = %+v/%v", players, err)
	}
	for _, action := range []func() error{
		func() error { return adapter.Kick(ctx, "Steve", "bad") },
		func() error { return adapter.Ban(ctx, "Alex", "bad") },
		func() error { return adapter.Pardon(ctx, "Alex") },
		func() error { return adapter.SetOperator(ctx, "Alex", 2, false) },
		func() error { return adapter.SetOperator(ctx, "Alex", 2, true) },
		func() error { return adapter.SetWhitelist(ctx, "Alex", false) },
		func() error { return adapter.SetWhitelist(ctx, "Alex", true) },
	} {
		_ = action()
	}
	if err := adapter.Kick(ctx, "Steve", "bad"); err == nil {
		t.Fatal("expected exec error")
	} else {
		assertNoSecret(t, err.Error(), info.ManagementSecret)
	}
	want := []string{
		cmdListUUIDs, "kick Steve bad", "ban Alex bad", "pardon Alex", "op Alex", "deop Alex",
		"whitelist add Alex", "whitelist remove Alex", "kick Steve bad",
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %+v, want %+v", commands, want)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}
