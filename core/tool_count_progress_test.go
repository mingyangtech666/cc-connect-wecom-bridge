package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type countProgressTestPlatform struct {
	*stubPlatformEngine
	finishError    bool
	progressFrames []string
}

type countProgressTestHandle struct {
	platform *countProgressTestPlatform
	index    int
}

func (p *countProgressTestPlatform) StartToolCountProgress(_ context.Context, _ any, content string) (ToolCountProgress, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := len(p.sent)
	p.sent = append(p.sent, content)
	p.progressFrames = append(p.progressFrames, content)
	return &countProgressTestHandle{platform: p, index: index}, nil
}

func (h *countProgressTestHandle) UpdateToolCount(_ context.Context, content string, finished bool) error {
	if finished && h.platform.finishError {
		return errors.New("stream expired")
	}
	h.platform.mu.Lock()
	defer h.platform.mu.Unlock()
	h.platform.sent[h.index] = content
	h.platform.progressFrames = append(h.platform.progressFrames, content)
	return nil
}

func TestToolCountProgress_CoalescesAndFinalizesExactCount(t *testing.T) {
	p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	w := newToolCountProgressWriter(context.Background(), p, "ctx", LangChinese, true)
	w.Update(1)
	w.Update(2)
	if got := p.getSent(); len(got) != 1 || got[0] != "⏳ 正在处理｜工具调用1次" {
		t.Fatalf("rapid updates must coalesce into one message: %v", got)
	}
	w.lastSent = time.Now().Add(-2 * time.Second)
	w.Update(3)
	if got := p.getSent(); len(got) != 1 || got[0] != "⏳ 正在处理｜工具调用3次" {
		t.Fatalf("progress must update in place: %v", got)
	}
	w.Update(4)
	w.Finalize(ProgressCardStateCompleted)
	w.Finalize(ProgressCardStateFailed)
	if got := p.getSent(); len(got) != 1 || got[0] != "✅ 处理完成｜共调用4次" {
		t.Fatalf("final count must be exact and finalized only once: %v", got)
	}
}

func TestToolCountProgress_FailedStreamPreservesFinalCount(t *testing.T) {
	p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}, finishError: true}
	w := newToolCountProgressWriter(context.Background(), p, "ctx", LangChinese, true)
	w.Update(1)
	w.Update(2)
	w.Finalize(ProgressCardStateCompleted)
	got := p.getSent()
	if len(got) != 2 || got[1] != "✅ 处理完成｜共调用2次" {
		t.Fatalf("expired streams must retain the final count: %v", got)
	}
}

func TestProcessInteractiveEvents_CompactKeepsCountWithoutToolDetails(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "interrupted"}[failed], func(t *testing.T) {
			p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
			e.SetDisplayConfig(DisplayCfg{Mode: "compact", ToolMessages: false})
			as := newControllableSession("s1")
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
			as.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "secret-workspace-command"}
			as.events <- Event{Type: EventToolResult, ToolResult: "secret-result"}
			as.events <- Event{Type: EventResult, Done: false}
			as.events <- Event{Type: EventToolUse, ToolName: "Read", ToolInput: "private-file-path"}
			terminal := Event{Type: EventResult, Content: "done", Done: true}
			want := "✅ 处理完成｜共调用2次"
			if failed {
				terminal = Event{Type: EventError, Error: errors.New("agent interrupted")}
				want = "⚠️ 处理中断｜已调用2次"
			}
			as.events <- terminal
			session := e.sessions.GetOrCreateActive("test:alice")
			e.processInteractiveEvents(state, session, e.sessions, "test:alice", "m1", time.Now(), nil, nil, "ctx")
			got := p.getSent()
			if len(got) != 2 || got[0] != want {
				t.Fatalf("count must remain visible when tool details are hidden: %v", got)
			}
			for _, text := range got {
				if strings.Contains(text, "secret") || strings.Contains(text, "private-file") || strings.Contains(text, "Bash") {
					t.Fatalf("tool details leaked: %q", text)
				}
			}
		})
	}
}

func testCUJCompactToolCounts(t *testing.T) {
	p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	agent := &cujAgent{}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	env := &cujEnv{t: t, engine: e, plat: p.stubPlatformEngine, agent: agent}
	send := func(text string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:alice", Platform: "test", UserID: "alice", Content: text, MessageID: text, ReplyCtx: text})
	}
	// Three user actions: enable compact mode, request tools, request tools again.
	send("/quiet compact")
	env.waitFor("compact acknowledgement", 2*time.Second, func() bool { return len(p.getSent()) > 0 })
	agent.setNextSessionEvents([]Event{
		{Type: EventThinking, Content: "Checking first task", PublicProgress: true},
		{Type: EventThinking, Content: "Checking first task", PublicProgress: true},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "do-not-show-command"},
		{Type: EventToolUse, ToolName: "Read", ToolInput: "do-not-show-workdir"},
		{Type: EventResult, Content: "first-answer", Done: true},
	}, 0)
	send("first request")
	env.waitFor("first answer", 2*time.Second, func() bool { return env.sentContains("first-answer") })
	if !env.sentContains("Completed | Tool calls: 2") {
		t.Fatalf("compact mode hid the actual tool count: %v", p.getSent())
	}
	agent.mu.Lock()
	session := agent.sessions[0]
	agent.mu.Unlock()
	session.mu.Lock()
	session.pendingEvents = []Event{
		{Type: EventThinking, Content: "Checking second task", PublicProgress: true},
		{Type: EventToolUse, ToolName: "Read", ToolInput: "do-not-show-path"},
		{Type: EventResult, Content: "second-answer", Done: true},
	}
	session.mu.Unlock()
	send("second request")
	env.waitFor("second answer", 2*time.Second, func() bool { return env.sentContains("second-answer") })
	if !env.sentContains("Completed | Tool calls: 1") {
		t.Fatalf("the next turn must start a fresh count: %v", p.getSent())
	}
	var progressCards int
	for _, text := range p.getSent() {
		if strings.Contains(text, "Completed | Tool calls:") {
			progressCards++
			if strings.Contains(text, "Tool calls: 2") && strings.Count(text, "Checking first task") != 1 {
				t.Fatalf("first task description must appear once: %q", text)
			}
			if strings.Contains(text, "Tool calls: 1") && (!strings.Contains(text, "Checking second task") || strings.Contains(text, "Checking first task")) {
				t.Fatalf("next turn must reset progress descriptions: %q", text)
			}
		} else if strings.Contains(text, "Checking ") {
			t.Fatalf("progress escaped its count card: %q", text)
		}
	}
	if progressCards != 2 {
		t.Fatalf("want exactly one progress card per task, got %d", progressCards)
	}
	for _, text := range p.getSent() {
		if strings.Contains(text, "do-not-show") || strings.Contains(text, "Bash") {
			t.Fatalf("compact mode leaked tool details: %q", text)
		}
	}
}
