package wecom

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func groupAccessOptions(dir string) map[string]any {
	return map[string]any{"bot_id": "bot-1", "bot_secret": "test-secret", "group_allowlist": true,
		"cc_data_dir": dir, "cc_project": "test-project"}
}

func groupAccessPlatform(t *testing.T, opts map[string]any) *WSPlatform {
	t.Helper()
	p, err := newWebSocket(opts)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*WSPlatform)
}

func TestGroupAllowlist_PersistsInviteAndRemovalAcrossRestart(t *testing.T) {
	opts := groupAccessOptions(t.TempDir())
	p := groupAccessPlatform(t, opts)
	group := wsReplyContext{chatType: "group", chatID: "group-a"}
	if _, allowed := p.GroupAccess(group); allowed {
		t.Fatal("new group allowed")
	}
	for i := 0; i < 2; i++ {
		if err := p.SetGroupAccess("group-a", true); err != nil {
			t.Fatal(err)
		}
	}
	p = groupAccessPlatform(t, opts)
	if _, allowed := p.GroupAccess(group); !allowed {
		t.Fatal("authorization lost after restart")
	}
	if _, allowed := p.GroupAccess(wsReplyContext{chatType: "group", chatID: "group-b"}); allowed {
		t.Fatal("authorization leaked to another group")
	}
	if err := p.SetGroupAccess("group-a", false); err != nil {
		t.Fatal(err)
	}
	p = groupAccessPlatform(t, opts)
	if _, allowed := p.GroupAccess(group); allowed {
		t.Fatal("revocation lost after restart")
	}
}

func TestGroupAllowlist_IsolatedByBotAndProject(t *testing.T) {
	opts := groupAccessOptions(t.TempDir())
	p := groupAccessPlatform(t, opts)
	if err := p.SetGroupAccess("group-a", true); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bot_id", "cc_project"} {
		other := groupAccessOptions(opts["cc_data_dir"].(string))
		other[key] = "other"
		if _, allowed := groupAccessPlatform(t, other).GroupAccess(wsReplyContext{chatType: "group", chatID: "group-a"}); allowed {
			t.Fatalf("allowlist shared across %s", key)
		}
	}
}

func TestGroupAllowlist_SaveFailureLeavesAuthorizationUnchanged(t *testing.T) {
	p := groupAccessPlatform(t, groupAccessOptions(t.TempDir()))
	if err := p.SetGroupAccess("group-a", true); err != nil {
		t.Fatal(err)
	}
	p.groupAllowlist.path = filepath.Join(t.TempDir(), "not-a-file")
	if err := os.Mkdir(p.groupAllowlist.path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.SetGroupAccess("group-a", false); err == nil {
		t.Fatal("expected save error")
	}
	if _, allowed := p.GroupAccess(wsReplyContext{chatType: "group", chatID: "group-a"}); !allowed {
		t.Fatal("failed save revoked group")
	}
	if err := p.SetGroupAccess("group-b", true); err == nil {
		t.Fatal("expected save error")
	}
	if _, allowed := p.GroupAccess(wsReplyContext{chatType: "group", chatID: "group-b"}); allowed {
		t.Fatal("failed save granted group")
	}
}

func TestGroupAllowlist_CorruptStoreFailsClosed(t *testing.T) {
	opts := groupAccessOptions(t.TempDir())
	p := groupAccessPlatform(t, opts)
	if err := p.SetGroupAccess("group-a", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.groupAllowlist.path, []byte("broken json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newWebSocket(opts); err == nil {
		t.Fatal("corrupt allowlist silently accepted")
	}
}

func TestGroupAllowlist_PrivateChatAndInvalidMetadata(t *testing.T) {
	p := groupAccessPlatform(t, groupAccessOptions(t.TempDir()))
	if _, allowed := p.GroupAccess(wsReplyContext{chatType: "single", chatID: "member"}); !allowed {
		t.Fatal("private chat blocked")
	}
	for _, ctx := range []any{nil, "group-a", wsReplyContext{chatType: "group"}, wsReplyContext{chatID: "group-a"}} {
		if _, allowed := p.GroupAccess(ctx); allowed {
			t.Fatalf("invalid group metadata allowed: %#v", ctx)
		}
	}
	defaultPlatform := groupAccessPlatform(t, map[string]any{"bot_id": "bot", "bot_secret": "test"})
	if defaultPlatform.GroupAccessEnabled() {
		t.Fatal("allowlist enabled without opt-in")
	}
	opts := groupAccessOptions(t.TempDir())
	opts["group_allowlist"] = "true"
	if _, err := newWebSocket(opts); err == nil {
		t.Fatal("invalid setting silently disabled group authorization")
	}
}

func TestGroupAllowlist_UnapprovedMediaSkipsDownloadAndKeepsActualCommand(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { downloads.Add(1) }))
	defer server.Close()
	p := groupAccessPlatform(t, groupAccessOptions(t.TempDir()))
	messages := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { messages <- msg }
	body := map[string]any{"msgid": "blocked-media", "chatid": "actual-group", "chattype": "group", "from": map[string]string{"userid": "roman"},
		"msgtype": "mixed", "mixed": map[string]any{"msg_item": []any{
			map[string]any{"msgtype": "text", "text": map[string]string{"content": "@bot-1 /invite group"}},
			map[string]any{"msgtype": "image", "image": map[string]string{"url": server.URL}},
		}}, "quote": map[string]any{"msgtype": "text", "text": map[string]string{"content": "/remove group"}}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	p.handleMsgCallback(wsFrame{Headers: wsFrameHeaders{ReqID: "request"}, Body: raw})
	select {
	case msg := <-messages:
		if msg.Content != "/invite group" || msg.ReplyCtx.(wsReplyContext).chatID != "actual-group" {
			t.Fatalf("unexpected management message: %#v", msg)
		}
		if len(msg.Images) != 0 || len(msg.Files) != 0 || downloads.Load() != 0 {
			t.Fatal("unauthorized attachment was downloaded")
		}
	case <-time.After(time.Second):
		t.Fatal("management command not forwarded")
	}
}
