// Package daemon starts and probes the detached wtable daemon.
package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"wtable/internal/config"
)

// Ensure starts the current executable as a detached daemon when the control
// socket is not accepting connections.
func Ensure(ctx context.Context, paths config.Paths) error {
	probe, err := net.DialTimeout("unix", paths.Socket, 200*time.Millisecond)
	if err == nil {
		probe.Close()
		return nil
	}
	if err = paths.Prepare(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("unix", paths.Socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("daemon 启动失败，请查看 %s", paths.Log)
}
