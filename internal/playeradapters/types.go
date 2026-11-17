// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Source identifiers used by CapabilitySet.ChosenSource and by provenance
// consumers that map to carbonpanelv1.PlayerDataSource.
const (
	SourceManagementProtocol = "management_protocol"
	SourceRCON               = "rcon"
	SourceExec               = "exec"
)

// ErrNoAdapter is returned when no source can be used without inventing
// capability or reaching for controller-local state.
var ErrNoAdapter = errors.New("playeradapters: no source adapter available")

// ServerInfo is the caller-owned description of the target server. The caller
// (PlayerService) is responsible for loading configuration and for supplying a
// node-scoped ExecFunc; this package performs no database or Docker lookups.
type ServerInfo struct {
	ServerID, NodeID, ContainerID, DataPath string
	Edition                                 string // "java" | "bedrock"; unknown is treated as bedrock/unsupported
	MCVersion                               string // e.g. "1.21.9", "1.21.8"
	RCONEnabled                             bool
	RCONHost                                string
	RCONPort                                int
	RCONPassword                            string
	ManagementEnabled                       bool
	ManagementURL                           string // ws(s):// controller-reachable endpoint
	ManagementSecret                        string
}

// ExecFunc runs a fixed, already-validated command on the owning node (for
// example via docker exec rcon-cli). It must not be a general command relay.
type ExecFunc func(ctx context.Context, command string) (string, error)

// ObservedPlayer is one name/UUID pair from a source. UUID is empty when the
// source did not provide one; a missing UUID is never guessed from the name.
type ObservedPlayer struct {
	Name, UUID string
}

// Adapter is the narrow interface consumed by PlayerService.
type Adapter interface {
	Source() string
	List(ctx context.Context) ([]ObservedPlayer, error)
	Kick(ctx context.Context, name, reason string) error
	Ban(ctx context.Context, name, reason string) error
	Pardon(ctx context.Context, name string) error
	SetOperator(ctx context.Context, name string, level int32, remove bool) error
	SetWhitelist(ctx context.Context, name string, remove bool) error
}

// UnsupportedCapability records an honest gap. Reason must be actionable and
// user-safe; it never contains a secret.
type UnsupportedCapability struct {
	Name, Reason string
}

// CapabilitySet is returned alongside every Negotiate result, including
// failures, so the service can render degraded/unsupported states.
type CapabilitySet struct {
	Management   bool
	RCON         bool
	Exec         bool
	ChosenSource string
	Detail       string
	Unsupported  []UnsupportedCapability
}

// Notification is a joined/left push signal from the management protocol. It
// is advisory: it carries its own observation time and source and must not be
// merged silently into a roster obtained at a different time.
type Notification struct {
	Kind       string // "joined" | "left"
	Name       string
	UUID       string
	Source     string
	ObservedAt time.Time
}

const redactionMarker = "********"

// redact replaces every non-empty secret with the fixed marker. It is applied
// to every externally produced error/detail string before it leaves the
// package.
func redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, redactionMarker)
	}
	return s
}

func (i ServerInfo) redact(s string) string {
	return redact(s, i.ManagementSecret, i.RCONPassword)
}

func (i ServerInfo) wrapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New(i.redact(err.Error()))
}

func (i ServerInfo) sourceError(source string, err error) error {
	if err == nil {
		return nil
	}
	// Cancellation is a control signal, not a configuration error: preserve it
	// so callers can use errors.Is(context.Canceled/DeadlineExceeded).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("playeradapters: %s: %w", source, i.wrapError(err))
}
