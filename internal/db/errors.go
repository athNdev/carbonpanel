// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import "errors"

// ErrScheduledTaskNotFound is returned when a scheduled task ID does not exist.
// It lets the RPC layer map a missing task to connect.CodeNotFound instead of
// an internal error.
var ErrScheduledTaskNotFound = errors.New("scheduled task not found")

// ErrStagedModApprovalMissing marks a reviewed mod row that predates durable
// approved-digest admission. Publication must not infer approval from the
// mutable sidecar; the operator has to review and stage the update again.
var ErrStagedModApprovalMissing = errors.New("staged reviewed mod has no durable approved digest")

// ErrStagedModApprovalInvalid marks an approved digest that is not a SHA-512.
var ErrStagedModApprovalInvalid = errors.New("staged reviewed mod approved digest is not a SHA-512")

// ErrStagedModVersionChange marks a runtime target change refused while a
// reviewed mod candidate is still staged against the current target.
var ErrStagedModVersionChange = errors.New("staged reviewed mod candidate blocks the runtime target change")

// ErrStagedModRuntimeTargetChanged marks a reviewed mod row whose durable
// admission target no longer matches the effective Docker target at
// publication. The operator must review and stage the update again.
var ErrStagedModRuntimeTargetChanged = errors.New("staged reviewed mod runtime target changed")

// ErrStagedModApprovalIdentityMissing marks a reviewed mod row that recorded an
// approved digest before the durable provider release identity was persisted.
// Publication must not infer that identity from the mutable sidecar; the
// operator has to review and stage the update again.
var ErrStagedModApprovalIdentityMissing = errors.New("staged reviewed mod has no durable approved release identity")
