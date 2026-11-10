// Package logging configures the process-wide slog logger.
//
// Output goes to stderr as one line per event:
//
//	2026-01-02 15:04:05.000 INFO  imap: login remote=10.0.0.5:53211 user=a@b.tld
//
// When stderr is connected to the systemd journal, the timestamp is dropped
// (journald adds its own) and every line carries an sd-daemon priority prefix
// ("<6>" etc.), so `journalctl -p warning` and the coloured output work.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ParseLevel maps a config value ("debug", "info", "warn", "error") to a slog
// level. An empty value means info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q (use debug, info, warn or error)", s)
}

// Setup installs the default logger writing to stderr at the given level.
func Setup(level slog.Level) {
	slog.SetDefault(slog.New(NewHandler(os.Stderr, level, underJournal())))
}

// underJournal reports whether stderr is a journald stream rather than a
// terminal. systemd sets JOURNAL_STREAM for services logging to the journal.
func underJournal() bool {
	if os.Getenv("JOURNAL_STREAM") == "" {
		return false
	}
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice == 0
}

type handler struct {
	mu      *sync.Mutex
	w       io.Writer
	level   slog.Level
	journal bool
	attrs   string // attributes from WithAttrs, already rendered
	group   string // key prefix from WithGroup
}

// NewHandler returns a slog handler writing compact "LEVEL message key=value"
// lines to w. With journal set, lines get an sd-daemon priority prefix
// instead of a timestamp.
func NewHandler(w io.Writer, level slog.Level, journal bool) slog.Handler {
	return &handler{mu: &sync.Mutex{}, w: w, level: level, journal: journal}
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	buf := make([]byte, 0, 256)

	prefix := ""
	if h.journal {
		prefix = priority(r.Level)
	} else {
		t := r.Time
		if t.IsZero() {
			t = time.Now()
		}
		buf = t.AppendFormat(buf, "2006-01-02 15:04:05.000 ")
	}
	buf = append(buf, prefix...)
	buf = append(buf, levelName(r.Level)...)

	// Keep multi-line messages (e.g. panic stacks) at the same priority.
	msg := strings.TrimRight(r.Message, "\n")
	buf = append(buf, strings.ReplaceAll(msg, "\n", "\n"+prefix)...)

	buf = append(buf, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		buf = appendAttr(buf, h.group, a)
		return true
	})
	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := *h
	buf := []byte(h.attrs)
	for _, a := range attrs {
		buf = appendAttr(buf, h.group, a)
	}
	h2.attrs = string(buf)
	return &h2
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.group = h.group + name + "."
	return &h2
}

// priority returns the sd-daemon prefix for a level (syslog severities).
func priority(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "<3>"
	case l >= slog.LevelWarn:
		return "<4>"
	case l >= slog.LevelInfo:
		return "<6>"
	}
	return "<7>"
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR "
	case l >= slog.LevelWarn:
		return "WARN  "
	case l >= slog.LevelInfo:
		return "INFO  "
	}
	return "DEBUG "
}

func appendAttr(buf []byte, group string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return buf
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			group += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			buf = appendAttr(buf, group, ga)
		}
		return buf
	}

	var val string
	switch a.Value.Kind() {
	case slog.KindDuration:
		val = a.Value.Duration().Round(time.Millisecond).String()
	case slog.KindTime:
		val = a.Value.Time().Format(time.RFC3339)
	default:
		val = a.Value.String()
	}

	buf = append(buf, ' ')
	buf = append(buf, group...)
	buf = append(buf, a.Key...)
	buf = append(buf, '=')
	if needsQuote(val) {
		return strconv.AppendQuote(buf, val)
	}
	return append(buf, val...)
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r <= ' ' || r == '"' || r == '=' || r == 0x7f {
			return true
		}
	}
	return false
}
