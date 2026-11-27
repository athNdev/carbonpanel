// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func adminAuditActor() context.Context {
	return auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "admin-1", Username: "root"})
}

func adminAuditRows(t *testing.T, store *storage.Store, event string) []storage.ActivityLog {
	t.Helper()
	var rows []storage.ActivityLog
	if err := store.DB().Where("event = ?", event).Find(&rows).Error; err != nil {
		t.Fatalf("query %s audit rows: %v", event, err)
	}
	return rows
}

func requireAuditRowForActor(t *testing.T, store *storage.Store, event, subjectType, subjectID, actorID, actorName string) storage.ActivityLog {
	t.Helper()
	rows := adminAuditRows(t, store, event)
	if len(rows) != 1 {
		t.Fatalf("%s audit rows = %d, want exactly one", event, len(rows))
	}
	row := rows[0]
	if row.ActorID != actorID || row.ActorName != actorName {
		t.Fatalf("%s actor = (%q, %q), want %s/%s", event, row.ActorID, row.ActorName, actorID, actorName)
	}
	if row.Event != event || row.SubjectTyp != subjectType || row.SubjectID != subjectID {
		t.Fatalf("%s row = event %q subject (%q, %q), want %q (%q, %q)",
			event, row.Event, row.SubjectTyp, row.SubjectID, event, subjectType, subjectID)
	}
	return row
}

func requireOneAdminAuditRow(t *testing.T, store *storage.Store, event, subjectType, subjectID string) storage.ActivityLog {
	t.Helper()
	return requireAuditRowForActor(t, store, event, subjectType, subjectID, "admin-1", "root")
}

func decodeAdminAuditProperties(t *testing.T, row storage.ActivityLog) map[string]any {
	t.Helper()
	if row.Properties == "" {
		return map[string]any{}
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(row.Properties), &props); err != nil {
		t.Fatalf("decode audit properties %q: %v", row.Properties, err)
	}
	return props
}

func TestNodeAdminMutationsWriteBoundedAuditRows(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	svc := NewNodeService(store, nil, logger.New())
	ctx := adminAuditActor()

	created, err := svc.CreateNode(ctx, connect.NewRequest(&v1.CreateNodeRequest{
		Name:        "audit-node",
		Host:        "tcp://192.168.1.50:2375",
		Enabled:     true,
		MaxMemoryMb: 1024,
	}))
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodeID := created.Msg.Node.Id
	createRow := requireOneAdminAuditRow(t, store, activity.EventNodeCreate, "node", nodeID)
	createProps := decodeAdminAuditProperties(t, createRow)
	if createProps["name"] != "audit-node" || createProps["is_local"] != false || createProps["enabled"] != true {
		t.Fatalf("node.create properties = %v, want the node name, local flag and enabled flag", createProps)
	}

	const secret = "tls-key-sentinel-never-audited"
	if _, err := svc.UpdateNode(ctx, connect.NewRequest(&v1.UpdateNodeRequest{
		Id:        nodeID,
		Name:      strPtr("renamed-audit-node"),
		TlsKey:    strPtr(secret),
		TlsCert:   strPtr("tls-cert-sentinel"),
		TlsCaCert: strPtr("tls-ca-sentinel"),
	})); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	updateRow := requireOneAdminAuditRow(t, store, activity.EventNodeUpdate, "node", nodeID)
	if strings.Contains(updateRow.Properties, secret) || strings.Contains(updateRow.Properties, "tls-cert-sentinel") {
		t.Fatalf("node.update audit leaked TLS material: %s", updateRow.Properties)
	}
	updateProps := decodeAdminAuditProperties(t, updateRow)
	if updateProps["tls_credentials_replaced"] != true {
		t.Fatalf("node.update properties = %v, want tls_credentials_replaced=true", updateProps)
	}
	changed, _ := updateProps["changed_fields"].(string)
	for _, want := range []string{"name", "tls_ca_cert", "tls_cert", "tls_key"} {
		if !strings.Contains(changed, want) {
			t.Fatalf("node.update changed_fields = %q, want %s", changed, want)
		}
	}

	if _, err := svc.DeleteNode(ctx, connect.NewRequest(&v1.DeleteNodeRequest{Id: nodeID})); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	deleteRow := requireOneAdminAuditRow(t, store, activity.EventNodeDelete, "node", nodeID)
	if props := decodeAdminAuditProperties(t, deleteRow); props["name"] != "renamed-audit-node" {
		t.Fatalf("node.delete properties = %v, want the deleted node name", props)
	}
}

func TestNodeTLSClearWritesBoundedAuditRow(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	svc := NewNodeService(store, nil, logger.New())
	ctx := adminAuditActor()

	const ca, cert, key = "ca-sentinel", "cert-sentinel", "key-sentinel"
	created, err := svc.CreateNode(ctx, connect.NewRequest(&v1.CreateNodeRequest{
		Name:       "tls-audit-node",
		Host:       "unix:///var/run/docker.sock",
		TlsEnabled: true,
		TlsCaCert:  ca,
		TlsCert:    cert,
		TlsKey:     key,
	}))
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodeID := created.Msg.Node.Id
	before, err := store.GetNode(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}

	if err := svc.ClearNodeTLSCredentials(context.Background(), nodeID, NodeTLSClearAction{
		OperatorID:            "operator-9",
		OperatorCanUpdateNode: true,
		TargetNodeName:        "tls-audit-node",
		ConfirmationPhrase:    NodeTLSClearConfirmationPhrase,
	}); err != nil {
		t.Fatalf("ClearNodeTLSCredentials: %v", err)
	}

	rows := adminAuditRows(t, store, activity.EventNodeTLSClear)
	if len(rows) != 1 {
		t.Fatalf("node.tls_credentials_clear rows = %d, want exactly one", len(rows))
	}
	row := rows[0]
	if row.ActorID != "operator-9" || row.SubjectTyp != "node" || row.SubjectID != nodeID {
		t.Fatalf("clear row = actor %q subject (%q, %q)", row.ActorID, row.SubjectTyp, row.SubjectID)
	}
	for _, sentinel := range []string{ca, cert, key} {
		if strings.Contains(row.Properties, sentinel) {
			t.Fatalf("clear audit leaked TLS credential %q: %s", sentinel, row.Properties)
		}
	}
	props := decodeAdminAuditProperties(t, row)
	if props["generation_before"] != float64(before.EndpointGeneration) {
		t.Fatalf("generation_before = %v, want %d", props["generation_before"], before.EndpointGeneration)
	}
	if after, _ := props["generation_after"].(float64); int64(after) <= before.EndpointGeneration {
		t.Fatalf("generation_after = %v, want > %d", props["generation_after"], before.EndpointGeneration)
	}
}

func TestRoleAdminMutationsWriteAuditRows(t *testing.T) {
	svc := setupRoleTestService(t)
	ctx := adminAuditActor()

	if err := svc.store.CreateUser(ctx, &storage.User{
		ID: "u-assign", Username: "assignee", AuthProvider: "local", IsActive: true,
	}); err != nil {
		t.Fatalf("CreateUser fixture: %v", err)
	}

	created, err := svc.CreateRole(ctx, connect.NewRequest(&v1.CreateRoleRequest{
		Name:        "audited-role",
		Description: "created by audit test",
		Permissions: []*v1.Permission{{Resource: rbac.ResourceServers, Action: rbac.ActionRead, ObjectId: "*"}},
	}))
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	roleID := created.Msg.Role.Id
	requireOneAdminAuditRow(t, svc.store, activity.EventRoleCreate, "role", roleID)

	updatedName := "audited-role-renamed"
	if _, err := svc.UpdateRole(ctx, connect.NewRequest(&v1.UpdateRoleRequest{
		Id: roleID, Name: &updatedName, Description: strPtr("updated by audit test"),
	})); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	requireOneAdminAuditRow(t, svc.store, activity.EventRoleUpdate, "role", roleID)

	if _, err := svc.UpdatePermissions(ctx, connect.NewRequest(&v1.UpdatePermissionsRequest{
		RoleName: updatedName,
		Permissions: []*v1.Permission{
			{Resource: rbac.ResourceServers, Action: rbac.ActionRead, ObjectId: "*"},
			{Resource: rbac.ResourceServers, Action: rbac.ActionUpdate, ObjectId: "*"},
		},
	})); err != nil {
		t.Fatalf("UpdatePermissions: %v", err)
	}
	requireOneAdminAuditRow(t, svc.store, activity.EventRolePermissionsUpdate, "role", updatedName)

	if _, err := svc.AssignRole(ctx, connect.NewRequest(&v1.AssignRoleRequest{
		UserId: "u-assign", RoleName: updatedName,
	})); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	assignRow := requireOneAdminAuditRow(t, svc.store, activity.EventRoleAssign, "user", "u-assign")
	if props := decodeAdminAuditProperties(t, assignRow); props["role_name"] != updatedName {
		t.Fatalf("role.assign properties = %v, want role_name=%s", props, updatedName)
	}

	if _, err := svc.UnassignRole(ctx, connect.NewRequest(&v1.UnassignRoleRequest{
		UserId: "u-assign", RoleName: updatedName,
	})); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}
	requireOneAdminAuditRow(t, svc.store, activity.EventRoleUnassign, "user", "u-assign")

	if _, err := svc.DeleteRole(ctx, connect.NewRequest(&v1.DeleteRoleRequest{Id: roleID})); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	requireOneAdminAuditRow(t, svc.store, activity.EventRoleDelete, "role", roleID)
}

func newAuditAuthFixture(t *testing.T) (*storage.Store, *auth.Manager) {
	t.Helper()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return store, manager
}

func TestUserAdminMutationsWriteAuditRows(t *testing.T) {
	store, manager := newAuditAuthFixture(t)
	svc := NewUserService(store, manager, logger.New())
	ctx := adminAuditActor()
	const password = "CorrectHorseBatteryStaple9!"

	created, err := svc.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{
		Username: "audit-user",
		Password: password,
	}))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	userID := created.Msg.User.Id
	createRow := requireOneAdminAuditRow(t, store, activity.EventUserCreate, "user", userID)
	if strings.Contains(createRow.Properties, password) {
		t.Fatal("user.create audit leaked the password")
	}
	if props := decodeAdminAuditProperties(t, createRow); props["username"] != "audit-user" {
		t.Fatalf("user.create properties = %v, want username", props)
	}

	email := "audit-user@example.invalid"
	if _, err := svc.UpdateUser(ctx, connect.NewRequest(&v1.UpdateUserRequest{
		Id: userID, Email: &email, IsActive: boolPtr(true),
	})); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	updateRow := requireOneAdminAuditRow(t, store, activity.EventUserUpdate, "user", userID)
	if strings.Contains(updateRow.Properties, email) {
		t.Fatal("user.update audit leaked the email address")
	}

	if _, err := svc.DeleteUser(ctx, connect.NewRequest(&v1.DeleteUserRequest{Id: userID})); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	deleteRow := requireOneAdminAuditRow(t, store, activity.EventUserDelete, "user", userID)
	if props := decodeAdminAuditProperties(t, deleteRow); props["username"] != "audit-user" {
		t.Fatalf("user.delete properties = %v, want username", props)
	}
}

func TestAuthAdminMutationsWriteAuditRows(t *testing.T) {
	store, manager := newAuditAuthFixture(t)
	svc := NewAuthService(store, manager, nil, nil, logger.New())
	user, err := manager.CreateLocalUser(context.Background(), "token-owner", "", "CorrectHorseBatteryStaple9!")
	if err != nil {
		t.Fatalf("CreateLocalUser fixture: %v", err)
	}
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: user.ID, Username: user.Username})

	if _, err := svc.UpdateAuthSettings(ctx, connect.NewRequest(&v1.UpdateAuthSettingsRequest{
		LocalAuthEnabled: boolPtr(true),
		SessionTimeout:   int32Ptr(3600),
	})); err != nil {
		t.Fatalf("UpdateAuthSettings: %v", err)
	}
	settingsRow := requireAuditRowForActor(t, store, activity.EventAuthSettingsUpdate, "settings", "auth", user.ID, user.Username)
	if props := decodeAdminAuditProperties(t, settingsRow); props["session_timeout"] != float64(3600) {
		t.Fatalf("auth.settings_update properties = %v, want session_timeout", props)
	}

	const pin = "246810"
	invite, err := svc.CreateInvite(ctx, connect.NewRequest(&v1.CreateInviteRequest{
		Description: "audit invite",
		Pin:         strPtr(pin),
		MaxUses:     3,
	}))
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	inviteID := invite.Msg.Invite.Id
	inviteCode := invite.Msg.Invite.Code
	inviteRow := requireAuditRowForActor(t, store, activity.EventInviteCreate, "invite", inviteID, user.ID, user.Username)
	for _, secret := range []string{pin, inviteCode} {
		if secret != "" && strings.Contains(inviteRow.Properties, secret) {
			t.Fatalf("auth.invite.create audit leaked invite material %q: %s", secret, inviteRow.Properties)
		}
	}
	if props := decodeAdminAuditProperties(t, inviteRow); props["has_pin"] != true || props["max_uses"] != float64(3) {
		t.Fatalf("auth.invite.create properties = %v, want has_pin and max_uses", props)
	}

	if _, err := svc.DeleteInvite(ctx, connect.NewRequest(&v1.DeleteInviteRequest{Id: inviteID})); err != nil {
		t.Fatalf("DeleteInvite: %v", err)
	}
	requireAuditRowForActor(t, store, activity.EventInviteDelete, "invite", inviteID, user.ID, user.Username)

	token, err := svc.CreateAPIToken(ctx, connect.NewRequest(&v1.CreateAPITokenRequest{
		Name: "audit-token", ExpiresInDays: int32Ptr(7),
	}))
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	tokenID := token.Msg.ApiToken.Id
	tokenRow := requireAuditRowForActor(t, store, activity.EventAPITokenCreate, "api_token", tokenID, user.ID, user.Username)
	if token.Msg.PlaintextToken == "" || strings.Contains(tokenRow.Properties, token.Msg.PlaintextToken) {
		t.Fatal("auth.api_token.create audit leaked the plaintext token")
	}

	if _, err := svc.DeleteAPIToken(ctx, connect.NewRequest(&v1.DeleteAPITokenRequest{Id: tokenID})); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	requireAuditRowForActor(t, store, activity.EventAuthTokenRevoked, "api_token", tokenID, user.ID, user.Username)
}

func TestAdminAuditFailureDoesNotBreakMutation(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.DB().Migrator().DropTable(&storage.ActivityLog{}); err != nil {
		t.Fatalf("drop activity_logs fixture: %v", err)
	}
	svc := NewNodeService(store, nil, logger.New())

	created, err := svc.CreateNode(adminAuditActor(), connect.NewRequest(&v1.CreateNodeRequest{
		Name:        "audit-failure-node",
		Host:        "unix:///var/run/docker.sock",
		Enabled:     false,
		MaxMemoryMb: 512,
	}))
	if err != nil {
		t.Fatalf("CreateNode must succeed when the audit write fails: %v", err)
	}
	if _, err := store.GetNode(context.Background(), created.Msg.Node.Id); err != nil {
		t.Fatalf("node was not persisted after audit failure: %v", err)
	}
}
