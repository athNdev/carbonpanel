// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"errors"

	"gorm.io/gorm"

	"github.com/athNdev/carbon-panel/internal/activity"
)

// AuditLogger is the narrow seam for the activity trail. Tests use a recorder;
// production uses the shared internal/activity writer.
type AuditLogger interface {
	Log(activity.Entry) error
}

type gormAuditLogger struct{ db *gorm.DB }

// NewAuditLogger returns the production audit writer. A nil database fails
// closed: an audited mutation must not be reported successful without a log.
func NewAuditLogger(db *gorm.DB) AuditLogger { return &gormAuditLogger{db: db} }

func (l *gormAuditLogger) Log(e activity.Entry) error {
	if l == nil || l.db == nil {
		return errors.New("worldview: audit database unavailable")
	}
	return activity.Log(l.db, e)
}
