package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPublicProgress_CommentaryIsDistinctFromReasoningAndFinal(t *testing.T) {
	for _, explicitPhase := range []bool{false, true} {
		for _, appServer := range []bool{false, true} {
			name := "exec"
			if appServer {
				name = "appserver"
			}
			if explicitPhase {
				name += "_explicit_phase"
			}
			t.Run(name, func(t *testing.T) {
				events := make(chan core.Event, 16)
				var completed, started func(map[string]any)
				var flushFinal func()
				messageType, toolType := "agent_message", "command_execution"
				if appServer {
					s := &appServerSession{events: events}
					completed, started, flushFinal = s.handleItemCompleted, s.handleItemStarted, s.flushPendingAsText
					messageType, toolType = "agentMessage", "commandExecution"
				} else {
					s := &codexSession{ctx: context.Background(), events: events}
					completed = func(item map[string]any) { s.handleItemCompleted(map[string]any{"item": item}) }
					started = func(item map[string]any) { s.handleItemStarted(map[string]any{"item": item}) }
					flushFinal = s.flushPendingAsText
				}
				reasoning := map[string]any{"type": "reasoning", "text": "private reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "private reasoning"}}}
				if appServer {
					reasoning["summary"] = []any{"private reasoning"}
				}
				completed(reasoning)
				commentary := map[string]any{"type": messageType, "text": "正在整理会议安排"}
				if explicitPhase {
					commentary["phase"] = "commentary"
				}
				completed(commentary)
				if explicitPhase && len(events) != 2 {
					t.Fatal("explicit public commentary must stream before the next tool")
				}
				started(map[string]any{"type": toolType, "command": "private command"})
				completed(map[string]any{"type": messageType, "text": "核对完成，正在整理结果"})
				completed(map[string]any{"type": messageType, "phase": "final_answer", "text": "final meeting arrangement"})
				flushFinal()
				wantTypes := []core.EventType{core.EventThinking, core.EventThinking, core.EventToolUse, core.EventText, core.EventText}
				if len(events) != len(wantTypes) {
					t.Fatalf("got %d events, want %d", len(events), len(wantTypes))
				}
				for i, wantType := range wantTypes {
					event := <-events
					if event.Type != wantType || event.PublicProgress != (i == 1 || i == 3) {
						t.Fatalf("event %d must distinguish public status from reasoning/final: %+v", i, event)
					}
				}
			})
		}
	}
}
