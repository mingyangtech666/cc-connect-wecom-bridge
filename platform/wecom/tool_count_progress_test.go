package wecom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func TestToolCountProgress_ReplacesSameStreamAndFinishes(t *testing.T) {
	type progressFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			MsgType string `json:"msgtype"`
			Stream  struct {
				ID      string `json:"id"`
				Finish  bool   `json:"finish"`
				Content string `json:"content"`
			} `json:"stream"`
		} `json:"body"`
	}
	frames := make(chan progressFrame, 3)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for range 3 {
			var frame progressFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			frames <- frame
			if err := conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0, "errmsg": "ok"}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p := &WSPlatform{conn: conn, toolCountProgress: true}
	go func() {
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			p.handleFrame(frame)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := p.StartToolCountProgress(ctx, wsReplyContext{reqID: "callback-1", chatID: "chat-1"}, "count 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.UpdateToolCount(ctx, "count 2", false); err != nil {
		t.Fatal(err)
	}
	if err := h.UpdateToolCount(ctx, "completed 3", true); err != nil {
		t.Fatal(err)
	}
	var streamID string
	for i, want := range []string{"count 1", "count 2", "completed 3"} {
		select {
		case f := <-frames:
			if i == 0 {
				streamID = f.Body.Stream.ID
			}
			if streamID == "" || f.Body.Stream.ID != streamID || f.Headers.ReqID != "callback-1" || f.Cmd != "aibot_respond_msg" || f.Body.MsgType != "stream" || f.Body.Stream.Content != want || f.Body.Stream.Finish != (i == 2) {
				t.Fatalf("progress must replace one stream then finish: %+v", f)
			}
		case <-ctx.Done():
			t.Fatal("progress frame missing")
		}
	}
}

func TestToolCountProgress_DisabledByDefaultAndRequiresCallback(t *testing.T) {
	p := &WSPlatform{}
	if h, err := p.StartToolCountProgress(context.Background(), nil, "count"); h != nil || err != nil {
		t.Fatalf("disabled progress must do nothing: %v %v", h, err)
	}
	p.toolCountProgress = true
	if _, err := p.StartToolCountProgress(context.Background(), wsReplyContext{chatID: "proactive"}, "count"); err == nil {
		t.Fatal("a proactive context cannot start a callback stream")
	}
}

func TestToolCountProgress_LongMarkdownContinuesWithoutLosingText(t *testing.T) {
	type progressFrame struct {
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			Stream struct {
				ID      string `json:"id"`
				Finish  bool   `json:"finish"`
				Content string `json:"content"`
			} `json:"stream"`
		} `json:"body"`
	}
	frames := make(chan progressFrame, 32)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var frame progressFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			frames <- frame
			if err := conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p := &WSPlatform{conn: conn, toolCountProgress: true}
	go func() {
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			p.handleFrame(frame)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, err := p.StartToolCountProgress(ctx, wsReplyContext{reqID: "callback-long", chatID: "chat-long"}, "count\n\n**开始核对**")
	if err != nil {
		t.Fatal(err)
	}
	body := "**开始核对**\n\n" + strings.Repeat("核对目标。", 6000)
	if err := h.UpdateToolCount(ctx, "count\n\n"+body, false); err != nil {
		t.Fatal(err)
	}
	body += "\n\n**继续核对**\n\n- 保留最后一条完整文字\n- 保留 Markdown 格式"
	if err := h.UpdateToolCount(ctx, "count 12\n\n"+body, false); err != nil {
		t.Fatal(err)
	}
	if err := h.UpdateToolCount(ctx, "completed 12\n\n"+body, true); err != nil {
		t.Fatal(err)
	}
	var order []string
	latest := make(map[string]progressFrame)
	for len(frames) > 0 {
		frame := <-frames
		if len(frame.Body.Stream.Content) > wecomProgressMaxBytes || !utf8.ValidString(frame.Body.Stream.Content) {
			t.Fatalf("each continuation must respect the UTF-8 byte limit: %d", len(frame.Body.Stream.Content))
		}
		if frame.Headers.ReqID != "callback-long" {
			t.Fatal("continuation lost its callback")
		}
		id := frame.Body.Stream.ID
		if previous, ok := latest[id]; ok {
			if previous.Body.Stream.Finish {
				t.Fatal("a completed page was rewritten")
			}
		} else {
			order = append(order, id)
		}
		latest[id] = frame
	}
	if len(order) < 2 {
		t.Fatal("long progress needs continuation cards")
	}
	var restored strings.Builder
	for _, id := range order {
		frame := latest[id]
		if !frame.Body.Stream.Finish {
			t.Fatal("all progress pages must finish")
		}
		_, page, _ := strings.Cut(frame.Body.Stream.Content, "\n\n")
		restored.WriteString(page)
	}
	if restored.String() != body {
		t.Fatal("long progress lost or duplicated original Markdown")
	}
	if !strings.HasPrefix(latest[order[len(order)-1]].Body.Stream.Content, "completed 12\n\n") {
		t.Fatal("last continuation lost the final count/status")
	}
}
