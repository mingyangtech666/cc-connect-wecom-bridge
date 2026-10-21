package core

import (
	"errors"
	"strings"
	"testing"
)

type groupAccessTestPlatform struct {
	stubPlatformEngine
	enabled bool
	groups  map[string]bool
	saveErr error
}

func (p *groupAccessTestPlatform) GroupAccessEnabled() bool { return p.enabled }
func (p *groupAccessTestPlatform) GroupAccess(ctx any) (string, bool) {
	id, _ := ctx.(string)
	return id, id == "" || p.groups[id]
}
func (p *groupAccessTestPlatform) SetGroupAccess(id string, allowed bool) error {
	if p.saveErr != nil {
		return p.saveErr
	}
	if allowed {
		p.groups[id] = true
	} else {
		delete(p.groups, id)
	}
	return nil
}

func TestGroupAccess_OnlyConfiguredAdminsCanManage(t *testing.T) {
	for _, adminFrom := range []string{"roman", ""} {
		for _, command := range []string{"/invite group", "/remove group"} {
			t.Run(adminFrom+command, func(t *testing.T) {
				p := &groupAccessTestPlatform{enabled: true, groups: map[string]bool{"approved": true}}
				e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
				e.SetAdminFrom(adminFrom)
				for _, group := range []string{"approved", "unapproved"} {
					e.ReceiveMessage(p, &Message{UserID: "member", ReplyCtx: group, Content: command})
				}
				if len(p.groups) != 1 || !p.groups["approved"] {
					t.Fatal("non-admin changed authorization")
				}
				if len(p.getSent()) != 2 {
					t.Fatal("each unauthorized command must receive a denial")
				}
				for _, reply := range p.getSent() {
					if !strings.Contains(reply, "requires admin privilege") {
						t.Fatalf("unexpected reply: %q", reply)
					}
				}
			})
		}
	}
}

func TestGroupAccess_RejectsInvalidTargetsAndPreservesAccessOnSaveFailure(t *testing.T) {
	for _, tc := range []struct {
		content, group, expected string
		saveErr                  error
	}{
		{"/invite group", "", "in the group", nil},
		{"/invite group other-group", "actual-group", "Use `/invite group`", nil},
		{"/invite admin", "actual-group", "Use `/invite group`", nil},
		{"/invite group", "actual-group", "could not be saved", errors.New("disk full")},
	} {
		t.Run(tc.content+tc.group, func(t *testing.T) {
			p := &groupAccessTestPlatform{enabled: true, groups: map[string]bool{}, saveErr: tc.saveErr}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetAdminFrom("roman")
			e.ReceiveMessage(p, &Message{UserID: "RoMaN", ReplyCtx: tc.group, Content: tc.content})
			if len(p.groups) != 0 {
				t.Fatal("invalid command changed authorization")
			}
			got := p.getSent()
			if len(got) != 1 || !strings.Contains(got[0], tc.expected) {
				t.Fatalf("reply=%v", got)
			}
		})
	}
}

func TestGroupAccess_BlocksCommandsMediaAndQuotedInvitations(t *testing.T) {
	p := &groupAccessTestPlatform{enabled: true, groups: map[string]bool{}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("roman")
	for _, content := range []string{"/help", "/shell whoami", "hello", ""} {
		e.ReceiveMessage(p, &Message{UserID: "roman", ReplyCtx: "group", Content: content,
			ExtraContent: "/invite group", Images: []ImageAttachment{{Data: []byte("img")}}})
	}
	if len(p.getSent()) != 4 || len(e.sessions.AllSessions()) != 0 {
		t.Fatal("blocked messages must receive denials without creating sessions")
	}
	for _, reply := range p.getSent() {
		if !strings.Contains(reply, "This group is not authorized") {
			t.Fatalf("unexpected reply: %q", reply)
		}
	}
	if len(p.groups) != 0 {
		t.Fatal("quoted invite authorized group")
	}
}

func TestGroupAccess_DisabledPreservesExistingBehavior(t *testing.T) {
	p := &groupAccessTestPlatform{groups: map[string]bool{}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	if e.handleGroupAccess(p, &Message{ReplyCtx: "group", Content: "/help"}) {
		t.Fatal("disabled gate consumed message")
	}
}
