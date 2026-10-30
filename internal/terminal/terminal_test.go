package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"wtable/internal/model"
)

// This exercises the real PTY, including a device reply that has to travel
// back to the process before it can produce any further output.
func TestPTYRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type exitResult struct {
		code int
		err  error
	}
	exited := make(chan exitResult, 1)
	script := `stty raw -echo
printf '\033[6n'
reply=$(dd bs=1 count=6 2>/dev/null)
[ "$reply" = "$(printf '\033[1;1R')" ] || exit 9
printf '\033[1m\033[38;2;12;34;56m中é🙂\033[0m\033[?2004h\033[?1h\033[?1000h\033[?1006h'
dd bs=1 count=1 >/dev/null 2>&1
printf '\033[?1049hALT'
dd bs=1 count=1 >/dev/null 2>&1
printf '\033[?1049l'
i=0; while [ "$i" -lt 20 ]; do printf '\r\nline-%s' "$i"; i=$((i+1)); done
printf '\r\nTAIL'
dd bs=1 count=1 >/dev/null 2>&1
printf '\r\nFINAL'
exit 37`
	term, err := Start(ctx, Config{
		Command: []string{"/bin/sh", "-c", script}, Cols: 24, Rows: 5,
		OnExit: func(code int, err error) { exited <- exitResult{code, err} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	await := func(check func(model.Frame) bool) model.Frame {
		t.Helper()
		for {
			f := term.Snapshot(0)
			if check(f) {
				return f
			}
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for frame: %v; got %q", ctx.Err(), frameText(f))
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	f := await(func(f model.Frame) bool { return f.BracketedPaste && strings.Contains(frameText(f), "中é🙂") })
	if f.Cells[0][0].Width != 2 || f.Cells[0][1].Width != 0 || f.Cells[0][2].Width != 1 || f.Cells[0][3].Width != 2 {
		t.Fatalf("wrong grapheme widths: %+v", f.Cells[0][:5])
	}
	if f.Cells[0][0].FG != 0x0c2238 || f.Cells[0][0].Attr&model.Bold == 0 || !f.AppCursor || f.MouseMode != 1000 || !f.MouseSGR {
		t.Fatalf("style/modes not preserved: %+v", f)
	}
	f.Cells[0][0].Text = "mutated"
	if term.Snapshot(0).Cells[0][0].Text != "中" {
		t.Fatal("snapshot aliases mutable state")
	}
	if err := term.Input([]byte("a")); err != nil {
		t.Fatal(err)
	}
	f = await(func(f model.Frame) bool { return f.Alternate && strings.Contains(frameText(f), "ALT") })
	if f.HistoryLines != 0 {
		t.Fatal("alternate screen exposes scrollback")
	}
	if err := term.Input([]byte("b")); err != nil {
		t.Fatal(err)
	}
	f = await(func(f model.Frame) bool { return !f.Alternate && strings.Contains(frameText(f), "TAIL") })
	if f.HistoryLines < 10 || frameText(term.Snapshot(3)) == frameText(f) {
		t.Fatal("physical scrollback is unavailable")
	}
	if err := term.Resize(40, 6); err != nil {
		t.Fatal(err)
	}
	if got := term.Snapshot(0); got.Cols != 40 || got.Rows != 6 || got.HistoryLines != f.HistoryLines {
		t.Fatalf("resize lost dimensions or history: %+v", got)
	}
	if err := term.Input([]byte("c")); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-exited:
		if r.code != 37 || r.err == nil {
			t.Fatalf("wrong exit result: %+v", r)
		}
		if !strings.Contains(frameText(term.Snapshot(0)), "FINAL") {
			t.Fatal("exit callback arrived before final output")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A blocked PTY read must wake on Close even while a child is idle.
	closed := make(chan struct{})
	idle, err := Start(ctx, Config{
		Command: []string{"/bin/sh", "-c", "sleep 30 & printf READY; wait"},
		OnExit:  func(int, error) { close(closed) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idle.Close() })
	if err := idle.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("Close did not wake the PTY reader and reap the process")
	}
}

func frameText(f model.Frame) string {
	var b strings.Builder
	for _, row := range f.Cells {
		for _, cell := range row {
			b.WriteString(cell.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
