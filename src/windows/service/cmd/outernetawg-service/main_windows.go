package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/control"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/platform"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const serviceName = "OuternetAWG"

// Set by build.ps1 from the Windows UI project's authoritative Version.
var ClientVersion = "dev"

type handler struct{ owner string }

func buildIdentity() string {
	return fmt.Sprintf("OuternetAWG Windows service %s; AmneziaWG v3.1.20260828; IPC v1", ClientVersion)
}

func main() {
	owner := flag.String("owner-sid", "", "Windows SID allowed to control this service")
	version := flag.Bool("version", false, "print build identity without loading Wintun or changing networking")
	flag.Parse()
	if *version {
		fmt.Println(buildIdentity())
		return
	}
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		fmt.Fprintln(os.Stderr, "Run install.ps1 as administrator. This executable must run under the Windows Service Control Manager.")
		os.Exit(1)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		fmt.Fprintln(os.Stderr, "The service must run as LocalSystem:", err)
		os.Exit(1)
	}
	directory, err := platform.SecureDirectory()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Protect service data directory:", err)
		os.Exit(1)
	}
	logPath := filepath.Join(directory, "service.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 2*1024*1024 {
		if err := os.Rename(logPath, logPath+".old"); err != nil {
			fmt.Fprintln(os.Stderr, "rotate service log:", err)
			os.Exit(1)
		}
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Open service log:", err)
		os.Exit(1)
	}
	defer logFile.Close()
	slog.SetDefault(slog.New(slog.NewTextHandler(logFile, nil)))
	if err := svc.Run(serviceName, &handler{owner: *owner}); err != nil {
		slog.Error("service failed", "error", err)
		os.Exit(1)
	}
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending, WaitHint: 15000}
	directory, err := platform.SecureDirectory()
	if err != nil {
		slog.Error("protect data directory", "error", err)
		return true, 1
	}
	c, err := control.New(platform.Store{Directory: directory}, platform.Connect)
	if err != nil {
		slog.Error("load config", "error", err)
		return true, 2
	}
	listener, err := platform.Listen(h.owner)
	if err != nil {
		slog.Error("open control pipe", "error", err)
		return true, 3
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- platform.Serve(ctx, listener, c) }()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				c.Refresh(now)
			}
		}
	}()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	slog.Info("service ready; tunnel disconnected", "version", ClientVersion)
	var serveErr error
running:
	for {
		select {
		case serveErr = <-done:
			break running
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				break running
			}
		}
	}
	changes <- svc.Status{State: svc.StopPending, WaitHint: 60000}
	cancel()
	// Cancellation prevents any in-flight connect from surviving service shutdown.
	if err := c.Close(); err != nil {
		slog.Error("stop tunnel", "error", err)
		return true, 4
	}
	if serveErr != nil {
		slog.Error("control server failed", "error", serveErr)
		return true, 5
	}
	slog.Info("service stopped")
	return false, 0
}
