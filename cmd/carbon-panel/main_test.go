// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

// TestShouldAutoStartOnBoot_SkipsDeepSleeping is the MINE-172 regression guard.
// A deeply-asleep server has a deliberately stopped container, so the old
// "auto_start && !detached" rule resurrected it on every panel restart — burning
// exactly the RAM deep sleep exists to release, and leaving the operator with a
// server they believed was asleep but was in fact running.
func TestShouldAutoStartOnBoot_SkipsDeepSleeping(t *testing.T) {
	srv := &storage.Server{
		Name:          "vanilla-1",
		AutoStart:     true,
		AutoDeepSleep: true,
		Status:        storage.StatusDeepSleep,
	}
	if shouldAutoStartOnBoot(srv) {
		t.Fatal("a deeply-asleep server must never be auto-started on boot")
	}
}

// TestShouldAutoStartOnBoot_StartsOrdinaryServers guards against over-correction:
// the deep-sleep exclusion must not disable auto-start for normal servers.
func TestShouldAutoStartOnBoot_StartsOrdinaryServers(t *testing.T) {
	cases := []struct {
		name string
		srv  *storage.Server
		want bool
	}{
		{"stopped with auto-start enabled", &storage.Server{AutoStart: true, Status: storage.StatusStopped}, true},
		{"running with auto-start enabled", &storage.Server{AutoStart: true, Status: storage.StatusRunning}, true},
		{"starting with auto-start enabled", &storage.Server{AutoStart: true, Status: storage.StatusStarting}, true},
		{"creating is left to the in-flight create", &storage.Server{AutoStart: true, Status: storage.StatusCreating}, false},
		{"auto-start disabled", &storage.Server{AutoStart: false, Status: storage.StatusStopped}, false},
		{"detached servers are left alone", &storage.Server{AutoStart: true, Detached: true, Status: storage.StatusStopped}, false},
		{"paused servers still auto-start", &storage.Server{AutoStart: true, Status: storage.StatusPaused}, true},
		{"nil server is safe", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoStartOnBoot(tc.srv); got != tc.want {
				t.Fatalf("shouldAutoStartOnBoot() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldAutoStartOnBoot_PausedIsNotExcluded documents why paused is treated
// differently from deep sleep: a paused server's container still exists and is
// only cgroup-frozen, so restarting the panel should bring it back, whereas a
// deep-slept container is gone.
func TestShouldAutoStartOnBoot_PausedIsNotExcluded(t *testing.T) {
	srv := &storage.Server{AutoStart: true, Status: storage.StatusPaused}
	if !shouldAutoStartOnBoot(srv) {
		t.Fatal("paused servers must remain eligible for auto-start")
	}
}

// mainCallPositions records the first call position of each `receiver.Method`
// in main() so tests can assert composition-root ordering.
func mainCallPositions(t *testing.T) map[string]token.Pos {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	positions := make(map[string]token.Pos)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			key := receiver.Name + "." + sel.Sel.Name
			if _, seen := positions[key]; !seen {
				positions[key] = call.Pos()
			}
			if key == "eventBus.Subscribe" && len(call.Args) == 1 {
				handler, ok := call.Args[0].(*ast.SelectorExpr)
				if !ok {
					return true
				}
				handlerReceiver, ok := handler.X.(*ast.Ident)
				if ok && handlerReceiver.Name == "taskScheduler" && handler.Sel.Name == "HandleServerEvent" {
					positions["subscribe-task-scheduler"] = call.Pos()
				}
			}
			return true
		})
	}
	return positions
}

func requireCallBefore(t *testing.T, positions map[string]token.Pos, first, second string) {
	t.Helper()
	firstPos, ok := positions[first]
	if !ok {
		t.Fatalf("%s call not found in main", first)
	}
	secondPos, ok := positions[second]
	if !ok {
		t.Fatalf("%s call not found in main", second)
	}
	if firstPos >= secondPos {
		t.Fatalf("%s must run before %s (positions %d, %d)", first, second, firstPos, secondPos)
	}
}

// TestStartupInstallsModUpdatePlannerBeforeEventProducers guards the
// composition-root order. rpc.NewServer installs the shared mod-update planner
// on the scheduler; until that happens an event-triggered policy that fires has
// no executor. Keep the construction before the scheduler subscription, the
// metrics collector start and the scheduler run loop.
func TestStartupInstallsModUpdatePlannerBeforeEventProducers(t *testing.T) {
	positions := mainCallPositions(t)
	requireCallBefore(t, positions, "rpc.NewServer", "subscribe-task-scheduler")
	requireCallBefore(t, positions, "rpc.NewServer", "metricsCollector.Start")
	requireCallBefore(t, positions, "rpc.NewServer", "taskScheduler.Start")
}

// TestShutdownStopsMetricsBeforeScheduler guards the LIFO defer order at the
// composition root. The scheduler consumes metrics, so the metrics producer must
// stop first: the scheduler's stop defer is registered before the collector
// starts, and the collector's stop defer after it. Stopping the scheduler first
// would leave the metrics producer active under a drained scheduler.
func TestShutdownStopsMetricsBeforeScheduler(t *testing.T) {
	positions := mainCallPositions(t)
	requireCallBefore(t, positions, "taskScheduler.Stop", "metricsCollector.Start")
	requireCallBefore(t, positions, "taskScheduler.Stop", "metricsCollector.Stop")
}
