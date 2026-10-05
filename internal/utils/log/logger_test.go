package log

import (
	"bytes"
	"errors"
	"log/slog"
	"regexp"
	"testing"
)

var ansi = regexp.MustCompile("\033\\[[0-9;]*m")

func TestHandlerRendersAttrs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := slog.New(&colorHandler{out: &buf, level: slog.LevelInfo}).With("iface", "en0")
	logger.Error("connect failed", "error", errors.New("boom"), "retry_in", "5s")
	logger.Debug("hidden")

	got := ansi.ReplaceAllString(buf.String(), "")
	// drop the timestamp
	if len(got) < 9 {
		t.Fatalf("unexpected output %q", got)
	}

	if want := "ERR connect failed iface=en0 retry_in=5s boom\n"; got[9:] != want {
		t.Errorf("got %q, want %q", got[9:], want)
	}
}
