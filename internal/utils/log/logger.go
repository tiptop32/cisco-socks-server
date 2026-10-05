package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

func Setup(out io.Writer, level slog.Level) {
	slog.SetDefault(slog.New(&colorHandler{out: out, level: level}))
}

// Colorize wraps s in an ANSI-256 foreground color escape.
func Colorize(s string, c int) string {
	return fmt.Sprintf("\033[38;5;%dm%s\033[0m", c, s)
}

// colorHandler renders single-line records:
//
//	HH:MM:SS LVL message key=value ... error
//
// Attrs are dimmed and the error attr is always last, in red. Groups are
// flattened (WithGroup is a no-op).
type colorHandler struct {
	out   io.Writer
	level slog.Level
	attrs []slog.Attr
}

func (h *colorHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *colorHandler) Handle(_ context.Context, r slog.Record) error {
	var (
		b      strings.Builder
		errStr string
	)

	b.WriteString(Colorize(r.Time.Format("15:04:05"), 7))
	b.WriteByte(' ')
	b.WriteString(formatLevel(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)

	appendAttr := func(a slog.Attr) bool {
		switch {
		case a.Equal(slog.Attr{}):
		case a.Key == "error":
			errStr = a.Value.String()
		default:
			b.WriteByte(' ')
			b.WriteString(Colorize(a.Key+"="+a.Value.String(), 8))
		}

		return true
	}

	for _, a := range h.attrs {
		appendAttr(a)
	}

	r.Attrs(appendAttr)

	if errStr != "" {
		b.WriteByte(' ')
		b.WriteString(Colorize(errStr, 1))
	}

	b.WriteByte('\n')

	// one Write per record: both stdout and the TUI log channel take it whole
	_, err := io.WriteString(h.out, b.String())

	return err
}

func (h *colorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &colorHandler{
		out:   h.out,
		level: h.level,
		// fresh slice: plain append could share a backing array between siblings
		attrs: slices.Concat(h.attrs, attrs),
	}
}

// WithGroup is a no-op: attrs are rendered flat.
func (h *colorHandler) WithGroup(string) slog.Handler {
	return h
}

func formatLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return Colorize("ERR", 9)
	case l >= slog.LevelWarn:
		return Colorize("WRN", 11)
	case l >= slog.LevelInfo:
		return Colorize("INF", 10)
	default:
		return Colorize("DBG", 8)
	}
}
