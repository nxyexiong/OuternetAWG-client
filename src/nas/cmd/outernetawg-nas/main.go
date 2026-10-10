package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/nas/internal/nas"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "outernetawg-nas:", err)
		os.Exit(1)
	}
}

func run() error {
	action := "run"
	if len(os.Args) > 2 {
		return errors.New("usage: outernetawg-nas [run|status|healthcheck]")
	}
	if len(os.Args) == 2 {
		action = os.Args[1]
	}
	switch action {
	case "run":
		config, err := nas.LoadConfig(os.Getenv)
		if err != nil {
			return err
		}
		lock, err := nas.PrepareRuntime(nas.RuntimeDirectory)
		if err != nil {
			return err
		}
		defer lock.Close()
		profile, err := nas.LoadProfile(config)
		if err != nil {
			return err
		}
		client := nas.Client{Config: config, Profile: profile, Open: nas.Open, Publish: func(s nas.Status) error {
			return nas.WriteStatus(nas.RuntimeDirectory, s)
		}}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.Run(ctx)
	case "status", "healthcheck":
		status, err := nas.ReadStatus(nas.RuntimeDirectory)
		if err != nil {
			return err
		}
		if action == "status" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(status); err != nil {
				return err
			}
		}
		return status.Check(time.Now(), action == "healthcheck")
	case "--help", "-h":
		fmt.Println("usage: outernetawg-nas [run|status|healthcheck]")
		return nil
	default:
		return errors.New("usage: outernetawg-nas [run|status|healthcheck]")
	}
}
