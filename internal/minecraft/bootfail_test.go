// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var bootStart = time.Date(2026, 11, 6, 5, 2, 49, 0, time.UTC)

func ts(minute, second int) string {
	return time.Date(2026, 11, 6, 5, minute, second, 0, time.UTC).Format(time.RFC3339Nano)
}

func TestDetectBootFailureFabricMissingDep(t *testing.T) {
	lines := []string{
		ts(2, 51) + " [init] Starting the Minecraft server...",
		ts(2, 56) + " [main/INFO]: Loading Minecraft 1.20.1 with Fabric Loader 0.19.5",
		ts(3, 10) + " [main/INFO]: Immediate reason: [HARD_DEP_NO_CANDIDATE chipped 3.0.7 {depends athena @ [>=3.1.1]}, ROOT_FORCELOAD_SINGLE chipped 3.0.7]",
		ts(3, 10) + " [main/INFO]:  - Mod 'Chipped' (chipped) 3.0.7 requires version 3.1.1 or later of athena, which is missing!",
	}
	reason, ok := DetectBootFailure(lines, bootStart)
	if !ok {
		t.Fatal("missing hard dependency must be detected")
	}
	if !strings.Contains(reason, "Chipped") || !strings.Contains(reason, "athena") {
		t.Fatalf("reason = %q, want the mod and the missing dependency named", reason)
	}
	if strings.Contains(reason, "[main/INFO]") {
		t.Fatalf("reason = %q, log prefix must be stripped", reason)
	}
}

func TestDetectBootFailureRunnerExitCode(t *testing.T) {
	sp := " "
	dockerLines := []string{
		ts(2, 51) + sp + "[init] Starting the Minecraft server...",
		ts(3, 5) + sp + `WARN mc-server-runner Minecraft server failed. {"exitCode": 1}`,
	}
	reason, ok := DetectBootFailure(dockerLines, bootStart)
	if !ok {
		t.Fatal("non-zero runner exit code must be detected")
	}
	if !strings.Contains(reason, "exit code 1") {
		t.Fatalf("reason = %q, want the exit code", reason)
	}

	clean := []string{
		ts(2, 51) + sp + "[init] Starting the Minecraft server...",
		ts(3, 5) + sp + `WARN mc-server-runner All good. {"exitCode": 0}`,
	}
	if _, ok := DetectBootFailure(clean, bootStart); ok {
		t.Fatal("exit code 0 must not be a failure")
	}
}

func TestDetectBootFailureIgnoresPreviousAttempt(t *testing.T) {
	// Attempt 1 crashed; attempt 2 (new marker) is booting fine. The old
	// fatal must not paint the new boot red.
	lines := []string{
		ts(1, 10) + " [init] Starting the Minecraft server...",
		ts(1, 20) + " [main/INFO]:  - Mod 'Chipped' (chipped) 3.0.7 requires version 3.1.1 or later of athena, which is missing!",
		ts(2, 51) + " [init] Starting the Minecraft server...",
		ts(2, 56) + " [main/INFO]: Loading Minecraft 1.20.1 with Fabric Loader 0.19.5",
		ts(2, 57) + " [main/INFO]: Preparing spawn area: 10%",
	}
	if _, ok := DetectBootFailure(lines, bootStart); ok {
		t.Fatal("previous attempt's failure must not match the new boot")
	}
}

func TestDetectBootFailureIgnoresStaleLines(t *testing.T) {
	sp := " "
	stale := []string{
		ts(1, 20) + sp + "[main/INFO]:  - Mod 'Chipped' (chipped) 3.0.7 requires version 3.1.1 or later of athena, which is missing!",
		ts(2, 56) + sp + "[main/INFO]: Loading Minecraft 1.20.1 with Fabric Loader 0.19.5",
	}
	if _, ok := DetectBootFailure(stale, bootStart); ok {
		t.Fatal("lines predating the container start must be ignored")
	}
	unmarked := []string{
		ts(2, 56) + sp + "[main/INFO]:  - Mod 'Chipped' (chipped) 3.0.7 requires version 3.1.1 or later of athena, which is missing!",
	}
	if _, ok := DetectBootFailure(unmarked, time.Time{}); !ok {
		t.Fatal("fresh fatal lines without any boot marker must still match")
	}
}

func TestDetectBootFailureIgnoresNoise(t *testing.T) {
	lines := []string{
		ts(2, 51) + " [init] Starting the Minecraft server...",
		ts(2, 56) + " [main/WARN]: Mod simplehats uses the version 0.4.0a which isn't compatible with Loader's extended semantic version format",
		ts(2, 56) + " [main/WARN]: - Mod 'ConquestReforged Fabric' (conquest) 1.7.0 recommends version 0.6.1+1.20.4 or later of animatica, which is missing!",
		ts(2, 57) + " [main/ERROR]: [STDERR]: at net.minecraft.class_3238.channelRead(class_3238.java:95)",
		ts(2, 58) + " [Server thread/ERROR]: Encountered an unexpected exception",
		`not-a-timestamp at all`,
		"",
	}
	if reason, ok := DetectBootFailure(lines, bootStart); ok {
		t.Fatalf("warnings and stack traces are not boot failures: %q", reason)
	}
}

func TestDetectBootFailureJVMOOM(t *testing.T) {
	lines := []string{
		ts(2, 51) + " [init] Starting the Minecraft server...",
		ts(3, 0) + " [main/ERROR]: java.lang.OutOfMemoryError: Java heap space",
	}
	reason, ok := DetectBootFailure(lines, bootStart)
	if !ok {
		t.Fatal("a JVM out-of-memory error must be detected")
	}
	if !strings.Contains(reason, "OutOfMemoryError") {
		t.Fatalf("reason = %q, want the OOM named", reason)
	}
}

func TestDetectBootFailureTickLoopWatchdog(t *testing.T) {
	lines := []string{
		ts(2, 51) + " [init] Starting the Minecraft server...",
		ts(3, 0) + " [main/ERROR]: Description: Exception in server tick loop",
	}
	reason, ok := DetectBootFailure(lines, bootStart)
	if !ok {
		t.Fatal("a tick-loop watchdog crash must be detected")
	}
	if !strings.Contains(reason, "tick loop") {
		t.Fatalf("reason = %q, want the tick loop named", reason)
	}
}

func TestDetectBootFailureRepeatedIdenticalStack(t *testing.T) {
	header := "[main/ERROR]: java.lang.NullPointerException: Cannot invoke \"net.minecraft.world.level.Level.dimension()\" because \"this.level\" is null"
	frame := "[main/ERROR]: \tat net.minecraft.server.MinecraftServer.runServer(MinecraftServer.java:1234)"
	lines := []string{
		ts(2, 51) + " [init] Starting the Minecraft server...",
	}
	for i := 0; i < 3; i++ {
		lines = append(lines, ts(3, i)+" "+header, ts(3, i)+" "+frame)
	}
	reason, ok := DetectBootFailure(lines, bootStart)
	if !ok {
		t.Fatal("the same stack repeating three times in one boot must be detected")
	}
	if !strings.Contains(reason, "NullPointerException") || !strings.Contains(reason, "3x") {
		t.Fatalf("reason = %q, want the repeated stack with its count", reason)
	}
}

func TestDetectBootFailureToleratesOneOffAndDistinctStacks(t *testing.T) {
	header := "[main/ERROR]: java.lang.IllegalStateException: incompatible mod set"
	frame := "[main/ERROR]: \tat net.minecraftforge.fml.ModLoader.gatherAndInitializeMods(ModLoader.java:56)"

	two := []string{ts(2, 51) + " [init] Starting the Minecraft server..."}
	for i := 0; i < 2; i++ {
		two = append(two, ts(3, i)+" "+header, ts(3, i)+" "+frame)
	}
	if reason, ok := DetectBootFailure(two, bootStart); ok {
		t.Fatalf("two identical stacks are not yet a loop: %q", reason)
	}

	distinct := []string{ts(2, 51) + " [init] Starting the Minecraft server..."}
	for i := 0; i < 3; i++ {
		distinct = append(distinct,
			ts(3, i)+" [main/ERROR]: java.lang.IllegalStateException: failure "+strconv.Itoa(i),
			ts(3, i)+" "+frame,
		)
	}
	if reason, ok := DetectBootFailure(distinct, bootStart); ok {
		t.Fatalf("three distinct stacks are not a repeated identical stack: %q", reason)
	}
}
