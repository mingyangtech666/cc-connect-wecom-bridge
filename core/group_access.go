package core

import (
	"log/slog"
	"strings"
)

// GroupAccessController optionally restricts inbound group messages. The platform
// derives the group ID from trusted message metadata, never from command text.
type GroupAccessController interface {
	GroupAccessEnabled() bool
	GroupAccess(replyCtx any) (groupID string, allowed bool)
	SetGroupAccess(groupID string, allowed bool) error
}

// handleGroupAccess runs before hooks, commands and agent/session creation.
// Management commands work in an unapproved group so admins can authorize it.
func (e *Engine) handleGroupAccess(p Platform, msg *Message) bool {
	gate, ok := p.(GroupAccessController)
	if !ok || !gate.GroupAccessEnabled() {
		return false
	}
	groupID, allowed := gate.GroupAccess(msg.ReplyCtx)
	parts := strings.Fields(msg.Content)
	if len(parts) > 0 && (strings.EqualFold(parts[0], "/invite") || strings.EqualFold(parts[0], "/remove")) {
		command := strings.ToLower(parts[0])
		if !e.isAdmin(msg.UserID) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, command))
			return true
		}
		if len(parts) != 2 || !strings.EqualFold(parts[1], "group") {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGroupAccessUsage))
			return true
		}
		if groupID == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGroupAccessGroupOnly))
			return true
		}
		grant := command == "/invite"
		if err := gate.SetGroupAccess(groupID, grant); err != nil {
			slog.Error("group authorization save failed", "project", e.name, "platform", p.Name(), "error", err)
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGroupAccessSaveFailed))
			return true
		}
		slog.Info("audit: group_authorization_changed", "project", e.name, "platform", p.Name(),
			"user_id", msg.UserID, "group_id", groupID, "allowed", grant)
		key := MsgGroupAccessRemoved
		if grant {
			key = MsgGroupAccessAdded
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(key))
		return true
	}
	if !allowed {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGroupAccessDenied))
		return true
	}
	return false
}
