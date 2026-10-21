package core

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// ToolCountProgressPlatform optionally displays counts independently of verbose
// tool messages. A nil handle means the feature is disabled for this platform.
type ToolCountProgressPlatform interface {
	StartToolCountProgress(ctx context.Context, replyCtx any, content string) (ToolCountProgress, error)
}

// ToolCountProgress replaces one progress message; finished closes its stream.
type ToolCountProgress interface {
	UpdateToolCount(ctx context.Context, content string, finished bool) error
}

type toolCountProgressWriter struct {
	ctx              context.Context
	platform         Platform
	replyCtx         any
	i18n             *I18n
	starter          ToolCountProgressPlatform
	handle           ToolCountProgress
	count            int
	attempted        bool
	failed           bool
	finalized        bool
	lastSent         time.Time
	descriptions     []string
	seenDescriptions map[string]bool
}

func newToolCountProgressWriter(ctx context.Context, p Platform, replyCtx any, lang Language, enabled bool) *toolCountProgressWriter {
	w := &toolCountProgressWriter{ctx: ctx, platform: p, replyCtx: replyCtx, i18n: NewI18n(lang)}
	if enabled {
		w.starter, _ = p.(ToolCountProgressPlatform)
	}
	return w
}

func (w *toolCountProgressWriter) Update(count int) {
	w.count = count
	w.updateRunning(false)
}

// AppendDescription accepts only public agent commentary. Tool inputs, outputs,
// and reasoning must never be passed here. Preserve every distinct Markdown step.
func (w *toolCountProgressWriter) AppendDescription(text string) bool {
	if w.starter == nil || w.finalized {
		return false
	}
	if strings.TrimSpace(text) == "" || isEllipsisOnly(text) {
		return true
	}
	if w.seenDescriptions == nil {
		w.seenDescriptions = make(map[string]bool)
	}
	if w.seenDescriptions[text] {
		return w.handle != nil || w.failed
	}
	w.seenDescriptions[text] = true
	w.descriptions = append(w.descriptions, text)
	w.updateRunning(true)
	return w.handle != nil || w.failed
}

func (w *toolCountProgressWriter) content(key MsgKey) string {
	content := w.i18n.Tf(key, w.count)
	if len(w.descriptions) > 0 {
		content += "\n\n" + strings.Join(w.descriptions, "\n\n")
	}
	return content
}

func (w *toolCountProgressWriter) updateRunning(force bool) {
	if w.starter == nil || w.finalized || w.failed {
		return
	}
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	content := w.content(MsgToolCountRunning)
	if !w.attempted {
		w.attempted = true
		var err error
		w.handle, err = w.starter.StartToolCountProgress(ctx, w.replyCtx, content)
		if err != nil {
			w.failed = true
			slog.Warn("tool count progress: start failed", "error", err)
		} else if w.handle == nil {
			// A platform may implement the interface but disable it in config.
			w.starter = nil
		}
		w.lastSent = time.Now()
		return
	}
	// Coalesce rapid calls while always forcing the exact final count below.
	if w.handle == nil || (!force && time.Since(w.lastSent) < time.Second) {
		return
	}
	if err := w.handle.UpdateToolCount(ctx, content, false); err != nil {
		w.failed = true
		slog.Warn("tool count progress: update failed", "error", err)
	}
	w.lastSent = time.Now()
}

func (w *toolCountProgressWriter) Finalize(state ProgressCardState) {
	if !w.attempted || w.finalized {
		return
	}
	w.finalized = true
	key := MsgToolCountCompleted
	if state != ProgressCardStateCompleted {
		key = MsgToolCountInterrupted
	}
	content := w.content(key)
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	if w.handle != nil {
		if err := w.handle.UpdateToolCount(ctx, content, true); err == nil {
			return
		} else {
			w.failed = true
			slog.Warn("tool count progress: finalize failed", "error", err)
		}
	}
	// An unavailable/expired stream must not make the final count disappear.
	if w.failed {
		fallbackCtx, fallbackCancel := context.WithTimeout(w.ctx, 5*time.Second)
		defer fallbackCancel()
		if err := w.platform.Send(fallbackCtx, w.replyCtx, content); err != nil {
			slog.Warn("tool count progress: final fallback failed", "error", err)
		}
	}
}
