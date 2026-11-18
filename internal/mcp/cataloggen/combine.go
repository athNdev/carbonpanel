// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

// combinedSpec is a generated, source-tagged tool declaration. The generator
// resolves precedence by source order, not by map iteration, so the output is
// deterministic and every duplicate is resolved explicitly.
type combinedSpec struct {
	Name        string
	Procedure   string
	Class       string
	Confirm     bool
	Description string
	Source      string
}

// sourcePriority defines deterministic precedence for duplicate procedures.
// The pinned 30-tool first slice always wins; family tools keep their own
// names and scopes; integration-owned additions are last.
var sourcePriority = []string{"first_slice", "server", "content", "admin", "stream", "integration"}

func priority(source string) int {
	for i, s := range sourcePriority {
		if s == source {
			return i
		}
	}
	return len(sourcePriority)
}
