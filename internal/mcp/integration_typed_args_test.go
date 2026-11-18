// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane: exact typed argument assertions.
//
// A substring of a serialized request is not evidence of field mapping. This
// file decodes the recorded typed protobuf request and asserts exact field
// names, values and types for every argument the tool declared. Safety-
// relevant object ids, boolean flags (true AND false), numeric caps, enums,
// bytes and repeated/map values are all checked at the typed level.
package mcp

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// fieldAlias maps a tool argument name to a different generated request field
// when the tool deliberately renames it (for example a generic "id" argument
// that maps to world_entry_id). Unlisted arguments must match a real field by
// proto or JSON name; the alias table is explicit and reviewable.
var fieldAlias = map[string]string{
	"id":            "", // resolved per-tool below
	"mod_id":        "mod_id",
	"task_id":       "task_id",
	"identity_keys": "identity_keys",
}

// toolFieldAlias resolves the small set of argument names whose generated
// request field differs from the argument itself.
func toolFieldAlias(tool, arg string) string {
	switch {
	case tool == "get_mod" && arg == "id":
		return "mod_id"
	case (tool == "get_world_entry" || tool == "list_world_revisions" || tool == "list_import_jobs") && arg == "id":
		return "world_entry_id"
	case arg == "id":
		switch tool {
		case "get_world_revision", "world_service_bind_world":
			return "revision_id"
		case "get_import_job":
			return "job_id"
		case "get_player", "get_player_skin":
			return "identity_key"
		}
		return "id"
	case tool == "update_user" && arg == "set_is_active":
		return "is_active"
	case tool == "world_view_service_set_map_overlay" && arg == "overlay_id":
		return "overlay/id"
	case arg == "server_id" && tool == "set_map_overlay":
		return "server_id"
	case arg == "kind" && tool == "world_service_resolve_world_operation":
		return "action"
	case arg == "undo_token" && tool == "player_service_undo_player_action":
		return "undo_token"
	case arg == "identity_key" && tool == "player_service_bulk_player_action":
		return "identity_keys"
	}
	if alias, ok := fieldAlias[arg]; ok && alias != "" {
		return alias
	}
	return arg
}

func findField(m protoreflect.Message, arg string) protoreflect.FieldDescriptor {
	fields := m.Descriptor().Fields()
	if fd := fields.ByTextName(arg); fd != nil {
		return fd
	}
	if fd := fields.ByJSONName(arg); fd != nil {
		return fd
	}
	return nil
}

// assertTypedFields checks every non-confirm argument against the exact typed
// request field. Missing fields, type mismatches, wrong values and unresolved
// argument names all fail the harness rather than being silently skipped.
func assertTypedFields(t *testing.T, tool string, args map[string]any, msg protoreflect.Message) {
	t.Helper()
	if msg == nil {
		t.Errorf("%s: no typed request was recorded", tool)
		return
	}
	for arg, want := range args {
		if arg == "confirm" || arg == "confirmation" {
			continue // consumed by the MCP gate, never mapped into the RPC
		}
		fieldName := toolFieldAlias(tool, arg)
		target := msg
		if idx := strings.Index(fieldName, "/"); idx >= 0 {
			// Explicit nested field path, e.g. overlay/id for the composed
			// MapOverlay message.
			parent := findField(msg, fieldName[:idx])
			if parent == nil || parent.Kind() != protoreflect.MessageKind || parent.IsMap() {
				t.Errorf("%s: argument %q has invalid nested path %q", tool, arg, fieldName)
				continue
			}
			target = msg.Mutable(parent).Message()
			fieldName = fieldName[idx+1:]
		}
		fd := findField(target, fieldName)
		if fd == nil {
			t.Errorf("%s: argument %q does not map to a request field (tried %q)", tool, arg, fieldName)
			continue
		}
		// set_is_active is a presence/control flag: it must set (or clear)
		// the oneof is_active field; its value comes from the separate
		// is_active argument. Assert presence, not a boolean value.
		if tool == "update_user" && arg == "set_is_active" {
			if !target.Has(fd) {
				t.Errorf("%s: set_is_active=true did not set the is_active presence", tool)
			}
			continue
		}
		assertTypedValue(t, tool, arg, want, target.Get(fd), fd)
	}
}

func assertTypedValue(t *testing.T, tool, arg string, want any, got protoreflect.Value, fd protoreflect.FieldDescriptor) {
	t.Helper()
	if fd.Kind() == protoreflect.MessageKind && fd.IsMap() {
		m, ok := want.(map[string]any)
		if !ok {
			t.Errorf("%s: field %s: map argument has type %T", tool, fd.Name(), want)
			return
		}
		gotMap := got.Map()
		if gotMap.Len() != len(m) {
			t.Errorf("%s: field %s: map has %d entries, want %d", tool, fd.Name(), gotMap.Len(), len(m))
		}
		for k, v := range m {
			gv := gotMap.Get(protoreflect.ValueOfString(k).MapKey())
			if !gv.IsValid() {
				t.Errorf("%s: field %s: map key %q missing", tool, fd.Name(), k)
				continue
			}
			assertTypedScalar(t, tool, string(fd.Name())+"/"+k, v, gv, fd.MapValue())
		}
		return
	}
	if fd.Cardinality() == protoreflect.Repeated {
		list, ok := want.([]any)
		if !ok {
			// A comma-separated identity list is the documented envelope for
			// bulk player actions.
			if s, isString := want.(string); isString {
				list = make([]any, 0)
				for _, part := range strings.Split(s, ",") {
					list = append(list, strings.TrimSpace(part))
				}
			} else {
				t.Errorf("%s: field %s: repeated argument has type %T", tool, fd.Name(), want)
				return
			}
		}
		gotList := got.List()
		if gotList.Len() != len(list) {
			t.Errorf("%s: field %s: got %d entries, want %d", tool, fd.Name(), gotList.Len(), len(list))
			return
		}
		for i, item := range list {
			assertTypedItem(t, fmt.Sprintf("%s[%d]", tool, i), arg, item, gotList.Get(i), fd)
		}
		return
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		s, ok := want.(string)
		if !ok || got.String() != s {
			t.Errorf("%s: field %s: got %q (%T), want %q", tool, fd.Name(), got.String(), want, s)
		}
	case protoreflect.BoolKind:
		b, ok := want.(bool)
		if !ok || got.Bool() != b {
			t.Errorf("%s: field %s: got %v (%T), want %v", tool, fd.Name(), got.Bool(), want, b)
		}
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		var wantInt int64
		switch v := want.(type) {
		case int:
			wantInt = int64(v)
		case int32:
			wantInt = int64(v)
		case int64:
			wantInt = v
		case float64:
			wantInt = int64(v)
		default:
			t.Errorf("%s: field %s: numeric argument has type %T", tool, fd.Name(), want)
			return
		}
		if got.Int() != wantInt {
			t.Errorf("%s: field %s: got %d, want %d", tool, fd.Name(), got.Int(), wantInt)
		}
	case protoreflect.DoubleKind, protoreflect.FloatKind:
		var wantFloat float64
		switch v := want.(type) {
		case float64:
			wantFloat = v
		case int:
			wantFloat = float64(v)
		default:
			t.Errorf("%s: field %s: float argument has type %T", tool, fd.Name(), want)
			return
		}
		if got.Float() != wantFloat {
			t.Errorf("%s: field %s: got %v, want %v", tool, fd.Name(), got.Float(), wantFloat)
		}
	case protoreflect.EnumKind:
		s, ok := want.(string)
		if !ok {
			t.Errorf("%s: field %s: enum argument has type %T", tool, fd.Name(), want)
			return
		}
		wantNum := enumNumber(fd.Enum(), s)
		if wantNum < 0 || got.Enum() != wantNum {
			t.Errorf("%s: field %s: got enum %d (%s), want %q -> %d", tool, fd.Name(),
				got.Enum(), fd.Enum().Values().ByNumber(got.Enum()), s, wantNum)
		}
	case protoreflect.BytesKind:
		s, ok := want.(string)
		if !ok {
			t.Errorf("%s: field %s: bytes argument has type %T", tool, fd.Name(), want)
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil || !equalBytes(got.Bytes(), decoded) {
			t.Errorf("%s: field %s: bytes mismatch (decode err=%v)", tool, fd.Name(), err)
		}
	case protoreflect.MessageKind:
		assertTypedMessage(t, tool, arg, want, got.Message())
	default:
		t.Errorf("%s: field %s: unsupported assertion kind %s", tool, fd.Name(), fd.Kind())
	}
}

func assertTypedItem(t *testing.T, tool, arg string, want any, got protoreflect.Value, fd protoreflect.FieldDescriptor) {
	t.Helper()
	switch fd.Kind() {
	case protoreflect.StringKind:
		s, _ := want.(string)
		if got.String() != s {
			t.Errorf("%s: field %s: got %q want %q", tool, arg, got.String(), s)
		}
	case protoreflect.EnumKind:
		s, _ := want.(string)
		if n := enumNumber(fd.Enum(), s); n < 0 || got.Enum() != n {
			t.Errorf("%s: field %s: got enum %d want %q", tool, arg, got.Enum(), s)
		}
	case protoreflect.MessageKind:
		if m, ok := want.(map[string]any); ok {
			assertTypedFields(t, tool+"/"+arg, m, got.Message())
		} else {
			t.Errorf("%s: field %s: repeated message item has type %T", tool, arg, want)
		}
	default:
		if fmt.Sprint(got.Interface()) != fmt.Sprint(want) {
			t.Errorf("%s: field %s: got %v want %v", tool, arg, got.Interface(), want)
		}
	}
}

func assertTypedMessage(t *testing.T, tool, arg string, want any, got protoreflect.Message) {
	t.Helper()
	switch w := want.(type) {
	case map[string]any:
		assertTypedFields(t, tool+"/"+arg, w, got)
	case []any:
		if len(w) != 1 {
			t.Errorf("%s: nested message argument %q must be a single object", tool, arg)
			return
		}
		m, ok := w[0].(map[string]any)
		if !ok {
			t.Errorf("%s: nested message argument %q is not an object", tool, arg)
			return
		}
		assertTypedFields(t, tool+"/"+arg, m, got)
	default:
		t.Errorf("%s: nested message argument %q has type %T", tool, arg, want)
	}
}

func assertTypedScalar(t *testing.T, tool, field string, want any, got protoreflect.Value, fd protoreflect.FieldDescriptor) {
	t.Helper()
	switch fd.Kind() {
	case protoreflect.StringKind:
		s, _ := want.(string)
		if got.String() != s {
			t.Errorf("%s: field %s: got %q want %q", tool, field, got.String(), s)
		}
	case protoreflect.BoolKind:
		b, _ := want.(bool)
		if got.Bool() != b {
			t.Errorf("%s: field %s: got %v want %v", tool, field, got.Bool(), b)
		}
	default:
		if fmt.Sprint(got.Interface()) != fmt.Sprint(want) {
			t.Errorf("%s: field %s: got %v want %v", tool, field, got.Interface(), want)
		}
	}
}

func enumNumber(ed protoreflect.EnumDescriptor, value string) protoreflect.EnumNumber {
	values := ed.Values()
	for i := 0; i < values.Len(); i++ {
		ev := values.Get(i)
		name := string(ev.Name())
		parts := strings.Split(name, "_")
		if strings.EqualFold(name, value) ||
			strings.EqualFold(parts[len(parts)-1], value) ||
			strings.EqualFold(strings.Join(parts[2:], "_"), value) {
			return ev.Number()
		}
	}
	return -1
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
