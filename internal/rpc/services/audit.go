// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
)

// auditPropertyStringMaxBytes bounds every string property so a console
// command or provider error cannot bloat the audit table. A longer value is
// cut on a UTF-8 boundary and ends with "..."; the result never exceeds the
// bound.
const auditPropertyStringMaxBytes = 512

// auditFields identifies one activity row apart from its properties. The
// actor always comes from the request context, never from a caller-supplied
// value.
type auditFields struct {
	IP         string
	Event      string
	SubjectTyp string
	SubjectID  string
	ServerID   string
	RequestID  string
}

// audit appends one activity row and never returns a failure: auditing must
// not break the operation being audited. ActorID/ActorName are read from the
// request context, RequestID is trimmed, and Properties is the JSON encoding
// of properties (empty when there are none). The server.command shape is
// {"command": string, "silent": bool, "success": bool, "error": string},
// where error is present only on a failed send.
func (s *ServerService) audit(ctx context.Context, fields auditFields, properties map[string]any) {
	actorID, actorName := "", ""
	if u := auth.GetUserFromContext(ctx); u != nil {
		actorID, actorName = u.ID, u.Username
	}

	props := ""
	if len(properties) > 0 {
		bounded := make(map[string]any, len(properties))
		for key, value := range properties {
			if text, ok := value.(string); ok {
				value = boundAuditString(text)
			}
			bounded[key] = value
		}
		encoded, err := json.Marshal(bounded)
		if err != nil {
			// Keep the row even when a value cannot be encoded; the event and
			// subject still matter for the audit trail.
			s.log.Error("Audit properties could not be encoded for %s: %v", fields.Event, err)
		} else {
			props = string(encoded)
		}
	}

	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         fields.IP,
		Event:      fields.Event,
		SubjectTyp: fields.SubjectTyp,
		SubjectID:  fields.SubjectID,
		ServerID:   fields.ServerID,
		Properties: props,
		RequestID:  strings.TrimSpace(fields.RequestID),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", fields.Event, err)
	}
}

func boundAuditString(value string) string {
	if len(value) <= auditPropertyStringMaxBytes {
		return value
	}
	const marker = "..."
	cut := auditPropertyStringMaxBytes - len(marker)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + marker
}
