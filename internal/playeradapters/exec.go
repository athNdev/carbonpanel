// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// execCallTimeout bounds one typed action over the node executor. The
// sender's command path may try a direct RCON dial first and fall back to
// docker exec (each with its own shorter bound), and an exec across a
// proxied or remote daemon costs several API round trips — the caller's
// budget must accommodate the whole sequence, not one hop.
const execCallTimeout = 10 * time.Second

// execAdapter uses a caller-supplied executor scoped to the owning node. It
// never falls back to a controller-local Docker socket or database.
type execAdapter struct {
	exec    ExecFunc
	info    ServerInfo
	timeout time.Duration
}

func (a *execAdapter) Source() string { return SourceExec }

func (a *execAdapter) run(ctx context.Context, command string) (string, error) {
	if a.exec == nil {
		return "", errors.New("playeradapters: exec: no executor supplied")
	}
	bounded, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	output, err := a.exec(bounded, command)
	if err != nil {
		return "", a.info.sourceError(SourceExec, err)
	}
	if len(output) > maxRCONOutput {
		return "", errors.New("playeradapters: exec: reply exceeds " + strconv.Itoa(maxRCONOutput) + " bytes")
	}
	return output, nil
}

func (a *execAdapter) List(ctx context.Context) ([]ObservedPlayer, error) {
	output, err := a.run(ctx, cmdListUUIDs)
	if err != nil {
		return nil, err
	}
	return parseListUUIDs(output)
}

func (a *execAdapter) Kick(ctx context.Context, name, reason string) error {
	command, err := buildKick(name, reason)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *execAdapter) Ban(ctx context.Context, name, reason string) error {
	command, err := buildBan(name, reason)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *execAdapter) Pardon(ctx context.Context, name string) error {
	command, err := buildPardon(name)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *execAdapter) SetOperator(ctx context.Context, name string, level int32, remove bool) error {
	command, err := operatorCommand(name, level, remove)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *execAdapter) SetWhitelist(ctx context.Context, name string, remove bool) error {
	command, err := buildWhitelist(name, remove)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}
