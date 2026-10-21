package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProcessInteractiveEvents_PublicProgressSharesCountCard(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_tools", true: "with_tools"}[withTools], func(t *testing.T) {
			p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
			e.SetDisplayConfig(DisplayCfg{Mode: "compact", ToolMessages: false})
			as := newControllableSession("s1")
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
			as.events <- Event{Type: EventThinking, Content: "正在核对会议安排", PublicProgress: true}
			as.events <- Event{Type: EventThinking, Content: "正在核对会议安排", PublicProgress: true}
			as.events <- Event{Type: EventThinking, Content: "private-reasoning"}
			if withTools {
				as.events <- Event{Type: EventToolUse, ToolInput: "private-workspace-command"}
				as.events <- Event{Type: EventToolResult, ToolResult: "private-tool-result"}
			}
			as.events <- Event{Type: EventText, Content: "核对完成，正在整理结果", PublicProgress: true}
			as.events <- Event{Type: EventText, Content: "最终会议安排"}
			as.events <- Event{Type: EventResult, Done: true}
			session := e.sessions.GetOrCreateActive("test:alice")
			e.processInteractiveEvents(state, session, e.sessions, "test:alice", "m1", time.Now(), nil, nil, "ctx")
			got := p.getSent()
			if len(got) != 2 {
				t.Fatalf("want one progress card and a separate final result: %v", got)
			}
			for _, description := range []string{"正在核对会议安排", "核对完成，正在整理结果"} {
				if strings.Count(got[0], description) != 1 || strings.Contains(got[1], description) {
					t.Fatalf("public description must appear once in the progress card: %v", got)
				}
			}
			wantCount := "共调用0次"
			if withTools {
				wantCount = "共调用1次"
			}
			if !strings.Contains(got[0], wantCount) || strings.Contains(got[0], "最终会议安排") || !strings.Contains(got[1], "最终会议安排") {
				t.Fatalf("counts and final result must stay in separate cards: %v", got)
			}
			if strings.Contains(strings.Join(got, "\n"), "private-") {
				t.Fatalf("reasoning/tool details leaked: %v", got)
			}
			if len(p.progressFrames) != 3 || !strings.Contains(p.progressFrames[0], "正在核对会议安排") {
				t.Fatalf("descriptions must stream immediately without duplicate updates: %v", p.progressFrames)
			}
		})
	}
}

func TestToolCountProgress_PublicDescriptionsPreserveAllMarkdown(t *testing.T) {
	p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	w := newToolCountProgressWriter(context.Background(), p, "ctx", LangChinese, true)
	w.AppendDescription(" \n … \t ")
	if len(p.getSent()) != 0 {
		t.Fatal("empty status must not create a card")
	}
	var descriptions []string
	for i := 0; i < 12; i++ {
		description := fmt.Sprintf("**步骤 %d**\n\n%s\n\n- 核对责任人\n- 核对截止时间\n\n```text\n  保留缩进\n```", i, strings.Repeat("正在整理会议安排，确认有没有偏离你的目标。", 4))
		descriptions = append(descriptions, description)
		w.AppendDescription(description)
	}
	w.AppendDescription(descriptions[0])
	w.Finalize(ProgressCardStateCompleted)
	got := p.getSent()
	want := "✅ 处理完成｜共调用0次\n\n" + strings.Join(descriptions, "\n\n")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("all twelve complete Markdown descriptions must survive without truncation or duplication: %v", got)
	}
}

type disabledCountProgressPlatform struct{ *stubPlatformEngine }

func (p *disabledCountProgressPlatform) StartToolCountProgress(context.Context, any, string) (ToolCountProgress, error) {
	return nil, nil
}

func TestToolCountProgress_DisabledDoesNotConsumePublicCommentary(t *testing.T) {
	p := &disabledCountProgressPlatform{&stubPlatformEngine{n: "test"}}
	w := newToolCountProgressWriter(context.Background(), p, "ctx", LangChinese, true)
	if w.AppendDescription("public status") {
		t.Fatal("disabled progress must preserve existing display behavior")
	}
	w.Update(1)
	w.Finalize(ProgressCardStateCompleted)
	if len(p.getSent()) != 0 {
		t.Fatalf("disabled progress created a card: %v", p.getSent())
	}
}

func TestToolCountProgress_InterruptedFallbackKeepsDescription(t *testing.T) {
	p := &countProgressTestPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}, finishError: true}
	w := newToolCountProgressWriter(context.Background(), p, "ctx", LangChinese, true)
	w.Update(1)
	w.AppendDescription("正在核对资料")
	w.Finalize(ProgressCardStateFailed)
	got := p.getSent()
	if len(got) != 2 || !strings.Contains(got[1], "处理中断｜已调用1次") || strings.Count(got[1], "正在核对资料") != 1 {
		t.Fatalf("expired stream must preserve interrupted count and description: %v", got)
	}
}
