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
)

func javaInfo() ServerInfo {
	return ServerInfo{
		ServerID:         "srv-1",
		Edition:          "java",
		MCVersion:        "1.21.9",
		RCONEnabled:      true,
		RCONHost:         "127.0.0.1",
		RCONPort:         25575,
		RCONPassword:     "rcon-password-value",
		ManagementURL:    "ws://127.0.0.1:25580",
		ManagementSecret: "management-secret-value",
	}
}

func TestNegotiateMatrix(t *testing.T) {
	execCalls := 0
	exec := func(context.Context, string) (string, error) {
		execCalls++
		return "There are 0 of a max of 20 players online: ", nil
	}

	tests := []struct {
		name       string
		edit       func(*ServerInfo)
		withExec   bool
		wantSource string
		wantErr    error
	}{
		{name: "java 1.21.9 management configured", edit: func(i *ServerInfo) { i.ManagementEnabled = true }, wantSource: SourceManagementProtocol},
		{name: "java 1.21.8 falls back to rcon", edit: func(i *ServerInfo) { i.MCVersion = "1.21.8" }, wantSource: SourceRCON},
		{name: "java no endpoint falls back to rcon", edit: func(i *ServerInfo) { i.ManagementURL = "" }, wantSource: SourceRCON},
		{name: "java no management secret falls back to rcon", edit: func(i *ServerInfo) { i.ManagementSecret = "" }, wantSource: SourceRCON},
		{name: "java rcon disabled uses exec when supplied", edit: func(i *ServerInfo) { i.RCONEnabled = false }, withExec: true, wantSource: SourceExec},
		{name: "java rcon without password uses exec", edit: func(i *ServerInfo) { i.RCONPassword = "" }, withExec: true, wantSource: SourceExec},
		{name: "java nothing available fails closed", edit: func(i *ServerInfo) {
			i.RCONEnabled = false
			i.ManagementEnabled = false
		}, wantErr: ErrNoAdapter},
		{name: "bedrock with exec still fails closed", edit: func(i *ServerInfo) { i.Edition = "bedrock" }, withExec: true, wantErr: ErrNoAdapter},
		{name: "unknown edition fails closed", edit: func(i *ServerInfo) { i.Edition = "geyser" }, withExec: true, wantErr: ErrNoAdapter},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := javaInfo()
			tc.edit(&info)
			var supplied ExecFunc
			if tc.withExec {
				supplied = exec
			}
			adapter, caps, err := Negotiate(context.Background(), info, supplied)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if adapter != nil {
					t.Fatal("expected no adapter on failure")
				}
				if caps.ChosenSource != "" || caps.Detail == "" {
					t.Fatalf("unexpected fail-closed capabilities: %+v", caps)
				}
			} else {
				if err != nil || adapter == nil {
					t.Fatalf("Negotiate: %v / %v", adapter, err)
				}
				if adapter.Source() != tc.wantSource || caps.ChosenSource != tc.wantSource {
					t.Fatalf("source = %q, caps = %+v, want %q", adapter.Source(), caps, tc.wantSource)
				}
				if caps.Detail == "" {
					t.Fatal("missing actionable detail")
				}
			}
			wantManagement := info.ManagementEnabled && strings.EqualFold(info.Edition, "java") && info.ManagementURL != "" && info.ManagementSecret != "" && info.MCVersion == "1.21.9"
			if caps.Management != wantManagement {
				t.Fatalf("caps.Management = %v, want %v (%+v)", caps.Management, wantManagement, caps)
			}
			gateNames := map[string]bool{}
			for _, gap := range caps.Unsupported {
				gateNames[gap.Name] = true
				if gap.Reason == "" {
					t.Fatalf("empty reason for %q", gap.Name)
				}
				assertNoSecret(t, gap.Reason, info.ManagementSecret, info.RCONPassword)
			}
			for _, name := range []string{"bridge.vitals", "bridge.inventory", "bridge.groups", "bridge.mute"} {
				if !gateNames[name] {
					t.Fatalf("missing decision gate %q in %+v", name, caps.Unsupported)
				}
			}
			assertNoSecret(t, caps.Detail, info.ManagementSecret, info.RCONPassword)
			if err != nil {
				assertNoSecret(t, err.Error(), info.ManagementSecret, info.RCONPassword)
			}
		})
	}
}

func TestNegotiateDecisionGateReasons(t *testing.T) {
	info := javaInfo()
	info.ManagementEnabled = true
	_, caps, err := Negotiate(context.Background(), info, nil)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	for _, gap := range caps.Unsupported {
		if strings.HasPrefix(gap.Name, "bridge.") || gap.Name == "bedrock.parity" {
			if gap.Reason != decisionPending {
				t.Fatalf("gap %q reason = %q, want %q", gap.Name, gap.Reason, decisionPending)
			}
		}
	}
}

func TestNegotiateRemoteExecOnlyWhenProvided(t *testing.T) {
	info := javaInfo()
	info.RCONEnabled = false
	calls := []string{}
	exec := func(_ context.Context, command string) (string, error) {
		calls = append(calls, command)
		return "There are 0 of a max of 20 players online: ", nil
	}
	adapter, caps, err := Negotiate(context.Background(), info, exec)
	if err != nil || caps.ChosenSource != SourceExec {
		t.Fatalf("expected exec adapter: %+v / %v", caps, err)
	}
	if _, err := adapter.List(context.Background()); err != nil {
		t.Fatalf("List via exec: %v", err)
	}
	if len(calls) != 1 || calls[0] != cmdListUUIDs {
		t.Fatalf("unexpected exec commands: %+v", calls)
	}

	_, _, err = Negotiate(context.Background(), info, nil)
	if !errors.Is(err, ErrNoAdapter) {
		t.Fatalf("expected ErrNoAdapter without an exec function, got %v", err)
	}
}

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		got, min string
		want     bool
		wantErr  bool
	}{
		{got: "1.21.9", min: minManagementJavaVersion, want: true},
		{got: "1.21.10", min: minManagementJavaVersion, want: true},
		{got: "1.22", min: minManagementJavaVersion, want: true},
		{got: "1.21.9-rc1", min: minManagementJavaVersion, want: true},
		{got: "1.21.8", min: minManagementJavaVersion, want: false},
		{got: "1.20.6", min: minManagementJavaVersion, want: false},
		{got: "", min: minManagementJavaVersion, wantErr: true},
		{got: "snapshot", min: minManagementJavaVersion, wantErr: true},
	}
	for _, tc := range tests {
		got, err := versionAtLeast(tc.got, tc.min)
		if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
			t.Errorf("versionAtLeast(%q,%q) = %v/%v, want %v/%v", tc.got, tc.min, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestNegotiateCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := Negotiate(ctx, javaInfo(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
