package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wtable/internal/config"
	"wtable/internal/model"
	"wtable/internal/server"
)

// Exercise the real daemon and Ghostty together: a resize must recover the
// authoritative screen, two idle streams must not delay each other, and closing
// the view must leave its process alive for the next desktop session.
func TestNativeTerminalDaemonBridge(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "wtable-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("WTABLE_HOME", home)
	t.Setenv("WTABLE_PEER_TOKEN", "")
	paths, err := config.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, paths) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon failed to stop")
		}
	})
	awaitNative(t, func() bool {
		c, err := net.DialTimeout("unix", paths.Socket, 20*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})
	app := NewApp()
	app.startup(ctx)
	if app.startupErr != nil {
		t.Fatal(app.startupErr)
	}
	t.Cleanup(func() { app.shutdown(context.Background()) })
	workspace := filepath.Join(home, "workspace")
	if _, err = app.CreateWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	spawn := func(name string) model.Pane {
		t.Helper()
		pane, err := app.SpawnPane(SpawnPaneRequest{Name: name, Command: []string{"/bin/sh", "-c", "stty -echo; printf 'RECOVERED-" + name + "\\r\\n'; exec /bin/cat"}})
		if err != nil {
			t.Fatal(err)
		}
		return pane
	}
	firstPane, secondPane := spawn("first"), spawn("second")
	first, err := app.OpenTerminal(ctx, firstPane)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := app.OpenTerminal(ctx, secondPane)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	awaitNative(t, func() bool {
		return strings.Contains(first.Terminal.Text(), "RECOVERED-first") && strings.Contains(second.Terminal.Text(), "RECOVERED-second")
	})
	// Changing the active workspace must not redirect an existing connection.
	app.setWorkspace(filepath.Join(home, "another-workspace"))
	if err = first.conn.Resize(40, 8); err != nil {
		t.Fatal(err)
	}
	awaitNative(t, func() bool {
		var frame model.Frame
		err := app.control.Call(ctx, "pane.screen", first.conn.params(), &frame)
		cols, rows := first.Terminal.Size()
		return err == nil && frame.Cols == 40 && frame.Rows == 8 && cols == 40 && rows == 8 && strings.Contains(first.Terminal.Text(), "RECOVERED-first")
	})
	// Once the first reader is idle in its long poll, the second stays responsive.
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	second.Terminal.Send([]byte("LIVE-second\n"))
	awaitNative(t, func() bool { return strings.Contains(second.Terminal.Text(), "LIVE-second") })
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("second pane blocked behind idle first pane: %v", elapsed)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if first.Status() != "closed" {
		t.Fatal("closed view did not report closed")
	}
	reopened, err := app.OpenTerminal(ctx, firstPane)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	awaitNative(t, func() bool { return strings.Contains(reopened.Terminal.Text(), "RECOVERED-first") })
	reopened.Terminal.Send([]byte("AFTER-REOPEN\n"))
	awaitNative(t, func() bool { return strings.Contains(reopened.Terminal.Text(), "AFTER-REOPEN") })
	app.shutdown(context.Background())
	if reopened.Status() != "closed" || second.Status() != "closed" {
		t.Fatal("desktop shutdown did not close every view")
	}
	// A fresh desktop can claim the writer and recover the same live daemon.
	nextApp := NewApp()
	nextApp.startup(ctx)
	if nextApp.startupErr != nil {
		t.Fatal(nextApp.startupErr)
	}
	defer nextApp.shutdown(context.Background())
	next, err := nextApp.OpenTerminal(ctx, firstPane)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	awaitNative(t, func() bool { return strings.Contains(next.Terminal.Text(), "AFTER-REOPEN") })
	next.Terminal.Send([]byte("NEXT-DESKTOP\n"))
	awaitNative(t, func() bool { return strings.Contains(next.Terminal.Text(), "NEXT-DESKTOP") })
}

func awaitNative(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for native terminal bridge")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
