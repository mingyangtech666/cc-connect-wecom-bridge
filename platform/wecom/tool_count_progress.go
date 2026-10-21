package wecom

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// WeCom's stream content limit is measured in UTF-8 bytes.
const wecomProgressMaxBytes = 20480

type wsToolCountProgress struct {
	platform          *WSPlatform
	reply             wsReplyContext
	streamID          string
	archivedBodyBytes int
}

func (p *WSPlatform) StartToolCountProgress(ctx context.Context, rctx any, content string) (core.ToolCountProgress, error) {
	if !p.toolCountProgress {
		return nil, nil
	}
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return nil, fmt.Errorf("wecom-ws: invalid tool count reply context %T", rctx)
	}
	if rc.reqID == "" {
		return nil, fmt.Errorf("wecom-ws: tool count stream requires a callback req_id")
	}
	progress := &wsToolCountProgress{platform: p, reply: rc, streamID: p.generateReqID("tool_count")}
	if err := progress.UpdateToolCount(ctx, content, false); err != nil {
		return nil, err
	}
	return progress, nil
}

func (s *wsToolCountProgress) UpdateToolCount(ctx context.Context, content string, finished bool) error {
	// The first paragraph is the changing count/status header. Everything after
	// it is append-only public Markdown. Freeze full pages, then continue in a
	// new stream so long progress is preserved without exceeding the API limit.
	header, body, _ := strings.Cut(content, "\n\n")
	if s.archivedBodyBytes > len(body) {
		return fmt.Errorf("wecom-ws: progress body shrank after pagination")
	}
	pages := splitByBytes(body[s.archivedBodyBytes:], wecomProgressMaxBytes-len(header)-2)
	for i, page := range pages {
		pageContent := header
		if page != "" {
			pageContent += "\n\n" + page
		}
		more := i < len(pages)-1
		if err := s.writePage(ctx, pageContent, finished || more); err != nil {
			return err
		}
		if more {
			s.archivedBodyBytes += len(page)
			s.streamID = s.platform.generateReqID("tool_count")
		}
	}
	return nil
}

func (s *wsToolCountProgress) writePage(ctx context.Context, content string, finished bool) error {
	frame := map[string]any{
		"cmd":     "aibot_respond_msg",
		"headers": map[string]string{"req_id": s.reply.reqID},
		"body": map[string]any{
			"msgtype": "stream",
			"stream": map[string]any{
				"id":      s.streamID,
				"finish":  finished,
				"content": content,
			},
		},
	}
	return s.platform.writeAndWaitAckStrict(ctx, frame, s.reply.reqID, wsAckTimeout)
}
