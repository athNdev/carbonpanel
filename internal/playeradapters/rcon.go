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

	"github.com/athNdev/carbon-panel/internal/rcon"
)

// rconCallTimeout bounds one typed action over a direct RCON connection.
// Dialing an unreachable RCON endpoint can cost the dial timeout by itself,
// and the caller falls back to docker exec afterwards, so this budget covers
// connect + authenticate + command rather than a single hop.
const rconCallTimeout = 10 * time.Second

// sendFunc is seam for the real RCON client and for fixtures.
type sendFunc func(ctx context.Context, info ServerInfo, command string) (string, error)

func rconSend(ctx context.Context, info ServerInfo, command string) (string, error) {
	return rcon.SendCommand(ctx, info.RCONHost, info.RCONPort, info.RCONPassword, command)
}

type rconAdapter struct {
	info    ServerInfo
	send    sendFunc
	timeout time.Duration
}

func (a *rconAdapter) Source() string { return SourceRCON }

func (a *rconAdapter) run(ctx context.Context, command string) (string, error) {
	if a.send == nil {
		return "", errors.New("playeradapters: rcon sender is nil")
	}
	bounded, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	output, err := a.send(bounded, a.info, command)
	if err != nil {
		return "", a.info.sourceError(SourceRCON, err)
	}
	if len(output) > maxRCONOutput {
		return "", errors.New("playeradapters: rcon: reply exceeds " + strconv.Itoa(maxRCONOutput) + " bytes")
	}
	return output, nil
}

func (a *rconAdapter) List(ctx context.Context) ([]ObservedPlayer, error) {
	output, err := a.run(ctx, cmdListUUIDs)
	if err != nil {
		return nil, err
	}
	return parseListUUIDs(output)
}

func (a *rconAdapter) Kick(ctx context.Context, name, reason string) error {
	command, err := buildKick(name, reason)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *rconAdapter) Ban(ctx context.Context, name, reason string) error {
	command, err := buildBan(name, reason)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *rconAdapter) Pardon(ctx context.Context, name string) error {
	command, err := buildPardon(name)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *rconAdapter) SetOperator(ctx context.Context, name string, level int32, remove bool) error {
	command, err := operatorCommand(name, level, remove)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

func (a *rconAdapter) SetWhitelist(ctx context.Context, name string, remove bool) error {
	command, err := buildWhitelist(name, remove)
	if err != nil {
		return err
	}
	_, err = a.run(ctx, command)
	return err
}

// operatorCommand maps the typed operator level to the single Java op/deop
// command: RCON cannot set a level, so levels >= 1 are reported as plain op by
// the caller's provenance. Level 0 with remove=false is a plain op grant.
func operatorCommand(name string, level int32, remove bool) (string, error) {
	if level < 0 || level > 4 {
		return "", errors.New("playeradapters: invalid operator level")
	}
	if remove {
		return buildDeop(name)
	}
	return buildOp(name)
}
