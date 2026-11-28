// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/proxy"
	rcon "github.com/athNdev/carbon-panel/internal/rcon"
	"github.com/moby/moby/client"
)

type DockerExecutor interface {
	ExecCommand(ctx context.Context, containerID string, command string) (string, error)
}

// containerStatusChecker is the optional container-inspect surface of a
// DockerExecutor; *docker.Client implements it. The sender uses it to catch
// a container paused outside the panel, where the DB status alone would not.
type containerStatusChecker interface {
	GetContainerStatus(ctx context.Context, containerID string) (storage.ServerStatus, error)
}

// Tuning for the command sender's timeouts.
const (
	// execFallbackTimeout bounds the docker-exec fallback so a container
	// that never answers cannot hang the caller.
	execFallbackTimeout = 5 * time.Second
	// pauseCheckTimeout bounds the optional container inspect used to detect
	// a container paused outside the panel.
	pauseCheckTimeout = 2 * time.Second
)

// Status-aware command rejections. These exact messages are shared with the
// WebSocket console (internal/ws hub) so every console path reads the same.
var (
	// ErrServerPaused reports a command sent to a frozen (paused) server.
	ErrServerPaused = errors.New("server is frozen (paused); resume the server before sending commands")
)

// rejectFrozenServer refuses commands for a server that cannot run them: a
// frozen (paused) container has no live process to answer RCON, and docker
// exec into it is rejected by the daemon or queues on a cgroup-frozen
// process. The DB status covers the panel's own freeze decision; the
// container inspect (when the executor supports it) also catches a container
// paused outside the panel. Inspect failures fail open: this is a usability
// guard, not an authorization check.
//
// Deep sleep is deliberately not rejected here: the deep-sleep manager sends
// its graceful "save-all"/"stop" through this sender after pre-setting the
// DB status to deepsleep, and refusing those would break the sleep flow.
func (s *Sender) rejectFrozenServer(ctx context.Context, server *storage.Server, executor DockerExecutor) error {
	if server.Status == storage.StatusPaused {
		return ErrServerPaused
	}
	checker, ok := executor.(containerStatusChecker)
	if !ok {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, pauseCheckTimeout)
	defer cancel()
	status, err := checker.GetContainerStatus(checkCtx, server.ContainerID)
	if err != nil {
		return nil
	}
	if status == storage.StatusPaused {
		return ErrServerPaused
	}
	return nil
}

type Sender struct {
	store   *storage.Store
	config  *config.Config
	docker  DockerExecutor
	pool    *docker.ClientPool
	limiter *commandLimiter
}

func NewSender(store *storage.Store, cfg *config.Config, dockerExec DockerExecutor, pool *docker.ClientPool) *Sender {
	return &Sender{
		store:   store,
		config:  cfg,
		docker:  dockerExec,
		pool:    pool,
		limiter: newCommandLimiter(),
	}
}

// resolveExecutor returns the DockerExecutor for the given nodeID, preferring a
// strict node-specific client from the pool over the single default executor so
// commands are not silently sent to the wrong (local) daemon for a remote node.
func (s *Sender) resolveExecutor(nodeID string) (DockerExecutor, error) {
	executor, err := docker.NewExecutorLocator(s.pool, s.docker, s.store).ExecutorFor(context.Background(), nodeID)
	if err != nil {
		return nil, fmt.Errorf("docker executor unavailable for node %q: %w", nodeID, err)
	}
	return executor, nil
}

func (s *Sender) SendCommand(ctx context.Context, serverID string, command string) (string, error) {
	// Enforce the same bound for the generated RPC console and the WebSocket
	// console before either RCON or docker exec sees the command.
	if len(command) > maxCommandBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrCommandTooLong, len(command))
	}
	server, err := s.store.GetServer(ctx, serverID)

	if err != nil || server == nil {
		return "", fmt.Errorf("server container not found")
	}
	if server.ContainerID == "" {
		return "", fmt.Errorf("server container not found")
	}

	// Only validated servers create limiter keys. Checking before lookup would
	// let an unauthenticated caller grow the key map with arbitrary IDs.
	limitKey := "server:" + serverID
	if user := auth.GetUserFromContext(ctx); user != nil {
		limitKey = "user:" + user.ID
	}
	if s.limiter != nil && !s.limiter.allow(limitKey) {
		return "", fmt.Errorf("%w: key %s", ErrCommandRateLimited, limitKey)
	}

	executor, err := s.resolveExecutor(server.NodeID)
	if err != nil {
		return "", err
	}

	// A frozen (paused) server cannot run commands; refuse here with an
	// actionable error instead of letting RCON time out and exec fail with a
	// daemon-internal message.
	if err := s.rejectFrozenServer(ctx, server, executor); err != nil {
		return "", err
	}

	// old docker exec command. The exec runs under its own deadline so a
	// container that never answers (a wedge that raced the paused checks
	// above) cannot hang the caller: the Docker client honours ctx for
	// create/attach/inspect, and a read wedged inside the exec stream is
	// abandoned with this call's deadline instead of blocking forever.
	dockerExec := func(cause error) (string, error) {
		if executor == nil {
			if cause != nil {
				return "", fmt.Errorf("no docker executor available: %w", cause)
			}
			return "", fmt.Errorf("no docker executor available")
		}
		execCtx, cancel := context.WithTimeout(ctx, execFallbackTimeout)
		defer cancel()
		type execOutcome struct {
			output string
			err    error
		}
		done := make(chan execOutcome, 1)
		go func() {
			output, execErr := executor.ExecCommand(execCtx, server.ContainerID, command)
			done <- execOutcome{output: output, err: execErr}
		}()
		select {
		case out := <-done:
			if out.err != nil {
				if cause != nil {
					return "", fmt.Errorf("rcon path failed: %w; fallback exec failed: %v", cause, out.err)
				}
				return "", fmt.Errorf("fallback exec failed: %w", out.err)
			}
			return out.output, nil
		case <-execCtx.Done():
			execErr := fmt.Errorf("docker exec did not answer within %s", execFallbackTimeout)
			if cause != nil {
				return "", fmt.Errorf("rcon path failed: %w; fallback exec failed: %v", cause, execErr)
			}
			return "", execErr
		}
	}

	// Remote nodes run on isolated Docker bridge networks that are not routable
	// from the panel over IP. Execute commands directly via Docker exec rather
	// than waiting on an unroutable TCP RCON handshake.
	if server.NodeID != "" && s.store != nil {
		if node, err := s.store.GetNode(ctx, server.NodeID); err == nil && node != nil && !node.IsLocal {
			return dockerExec(nil)
		}
	}

	serverCfg, err := s.store.GetServerConfig(ctx, serverID)
	if err != nil {
		return dockerExec(fmt.Errorf("failed to load server config: %w", err))
	}

	if serverCfg != nil && serverCfg.EnableRCON != nil && !*serverCfg.EnableRCON {
		return dockerExec(fmt.Errorf("rcon is disabled for this server"))
	}

	var rconPort int
	if s.config != nil && s.config.Minecraft.GlobalConfig != nil {
		if v, ok := s.config.Minecraft.GlobalConfig["rconPort"]; ok && v != nil {
			switch t := v.(type) {
			case int:
				rconPort = t
			case int64:
				rconPort = int(t)
			case float64:
				rconPort = int(t)
			case string:
				if p, err := strconv.Atoi(t); err == nil {
					rconPort = p
				}
			}
		}
	}
	if serverCfg != nil && serverCfg.RCONPort != nil {
		rconPort = *serverCfg.RCONPort
	}

	var rconPassword string
	if s.config != nil && s.config.Minecraft.GlobalConfig != nil {
		if v, ok := s.config.Minecraft.GlobalConfig["rconPassword"]; ok && v != nil {
			if p, ok := v.(string); ok {
				rconPassword = p
			} else {
				rconPassword = fmt.Sprint(v)
			}
		}
	}
	if serverCfg != nil && serverCfg.RCONPassword != nil {
		rconPassword = *serverCfg.RCONPassword
	}

	var cli proxy.DockerInspector
	if dc, ok := executor.(interface{ GetDockerClient() *client.Client }); ok {
		cli = dc.GetDockerClient()
	}
	networkName := ""
	if s.config != nil {
		networkName = s.config.Docker.NetworkName
	}
	ip, err := proxy.GetContainerIP(cli, server.ContainerID, networkName)
	if err != nil {
		return dockerExec(fmt.Errorf("failed to resolve container ip: %w", err))
	}

	// run comamand in dedicated context with timeout
	rconCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	output, err := rcon.SendCommand(rconCtx, ip, rconPort, rconPassword, command)

	if err != nil {
		// Redact RCON password from error message to prevent secret leakage in logs (SEC-4)
		errMsg := err.Error()
		if rconPassword != "" && strings.Contains(errMsg, rconPassword) {
			err = errors.New(strings.ReplaceAll(errMsg, rconPassword, "********"))
		}
		return dockerExec(fmt.Errorf("rcon command failed: %w", err))
	}

	return output, nil
}
