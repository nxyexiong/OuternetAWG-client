package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/gateway/internal/gateway"
)

var version = "dev"

const usage = `OuternetAWG Linux gateway
  configure --file gateway.json    Save source/interface settings (service stopped)
  protect                          Install persistent forwarding guard
  run                              Run the service in the foreground
  import --file profile.conf       Import .conf/.vpn; use --file - for stdin
  connect                          Connect and persist automatic reconnect
  disconnect                       Disconnect and persist pause; keep protection
  status                           Print live status as JSON
  doctor                           Read settings, routes, firewall and sysctls
  unprotect --confirm-direct-routing
                                   Remove owned rules (service stopped)
  version                          Print build version
`

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "OuternetAWG:", err)
		os.Exit(1)
	}
}

func execute(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	action := args[0]
	if action == "version" || action == "--version" {
		fmt.Println("OuternetAWG gateway", version)
		return nil
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	var file, name string
	var confirm bool
	switch action {
	case "configure", "import":
		flags.StringVar(&file, "file", "", "input file or - for stdin")
		if action == "import" {
			flags.StringVar(&name, "name", "Gateway", "profile alias")
		}
	case "unprotect":
		flags.BoolVar(&confirm, "confirm-direct-routing", false, "allow direct forwarding after removing protection")
	case "run", "protect", "connect", "disconnect", "status", "doctor":
	default:
		return errors.New("unknown command; use help")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if err := gateway.RequireLinuxRoot(); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store := gateway.Store{Directory: gateway.DataDirectory}
	if action == "connect" || action == "disconnect" || action == "status" || action == "import" {
		request := gateway.Request{Action: action, Name: name}
		if action == "import" {
			data, err := readInput(file, gateway.MaxMessageSize)
			if err != nil {
				return err
			}
			request.Content = string(data)
		}
		response, err := gateway.Call(ctx, gateway.SocketPath, request)
		if err != nil {
			return err
		}
		return output(response.Status)
	}
	if action == "doctor" {
		return doctor(ctx, store)
	}
	lock, err := gateway.Lock(gateway.RuntimeDirectory)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = store.Init(); err != nil {
		return err
	}
	runner := gateway.ExecRunner{}
	config, configErr := store.LoadConfig()
	network := &gateway.LinuxNetwork{Config: config, Runner: runner}
	switch action {
	case "configure":
		data, err := readInput(file, gateway.MaxConfigSize)
		if err != nil {
			return err
		}
		next, err := gateway.DecodeConfig(data)
		if err != nil {
			return err
		}
		if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
			return configErr
		}
		network.Config = next
		guards := []gateway.Config{next}
		if configErr == nil {
			guards = append(guards, config)
		}
		// Protect the union while committing new selectors; never open a rollback gap.
		if err = network.ProtectConfigs(ctx, guards...); err != nil {
			return err
		}
		if err = network.ClearRouting(ctx); err != nil {
			return fmt.Errorf("remove previous source policies (old and new sources remain blocked): %w", err)
		}
		if err = store.SaveConfig(next); err != nil {
			return fmt.Errorf("save settings (old and new sources remain blocked): %w", err)
		}
		if err = network.Protect(ctx); err != nil {
			return fmt.Errorf("settings saved, but final guard application failed; sources remain blocked: %w", err)
		}
		fmt.Println("Settings saved; protected forwarding is blocked. Start the service, import a profile, then connect.")
		return nil
	case "protect":
		if configErr == nil {
			if err = network.Protect(ctx); err == nil {
				return nil
			}
			configErr = err
		}
		fallback := network.EmergencyGuard(ctx)
		if fallback == nil {
			return fmt.Errorf("guard settings failed; emergency protection blocks ALL forwarding until repaired: %w", configErr)
		}
		_, stopErr := runner.Run(ctx, "sysctl", []string{"-q", "-w", "net.ipv4.ip_forward=0", "net.ipv6.conf.all.forwarding=0"}, "")
		return errors.Join(configErr, fmt.Errorf("emergency firewall failed: %w", fallback), stopErr)
	case "unprotect":
		if !confirm {
			return errors.New("this permits direct routing; first disable/stop both gateway units, then pass --confirm-direct-routing")
		}
		state, err := store.LoadState()
		if err != nil {
			return err
		}
		state.Desired = false
		if err = store.SaveState(state); err != nil {
			return err
		}
		if err = network.Unprotect(ctx); err != nil {
			return err
		}
		fmt.Println("Owned protection and policy routes removed. Host sysctls were left unchanged. An enabled guard unit will restore protection on reboot.")
		return nil
	case "run":
		if configErr != nil {
			return configErr
		}
		manager, err := gateway.NewManager(ctx, config, store, network, gateway.OpenEngine)
		if err != nil {
			return err
		}
		run, stop := context.WithCancel(ctx)
		defer stop()
		done := make(chan struct{})
		go func() {
			defer close(done)
			timer := time.NewTicker(2 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-timer.C:
					call, cancel := context.WithTimeout(run, 30*time.Second)
					manager.Tick(call, time.Now())
					cancel()
				case <-run.Done():
					return
				}
			}
		}()
		slog.Info("gateway control service ready", "ingress", config.IngressInterface)
		err = gateway.ServeControl(run, gateway.SocketPath, manager)
		stop()
		<-done
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return errors.Join(err, manager.Close(cleanup))
	}
	return errors.New("unsupported command")
}

func readInput(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New("--file is required")
	}
	var reader io.Reader = os.Stdin
	if path != "-" {
		file, err := os.Open(filepath.Clean(path))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds its size limit")
	}
	return data, nil
}

func output(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func doctor(ctx context.Context, store gateway.Store) error {
	config, err := store.LoadConfig()
	if err != nil {
		return err
	}
	if err = output(config); err != nil {
		return err
	}
	runner := gateway.ExecRunner{}
	var failures []error
	for _, command := range [][]string{
		{"ip", "-j", "address", "show"},
		{"ip", "-j", "-4", "route", "show", "table", "all"},
		{"ip", "-j", "-4", "rule", "show"},
		{"nft", "-j", "list", "table", "inet", "outernetawg_gateway"},
		{"sysctl", "net.ipv4.ip_forward", "net.ipv4.conf.all.send_redirects", "net.ipv4.conf." + config.IngressInterface + ".rp_filter"},
	} {
		fmt.Println("\n---", command, "---")
		data, err := runner.Run(ctx, command[0], command[1:], "")
		if err != nil {
			failures = append(failures, err)
			fmt.Fprintln(os.Stderr, err)
		} else if _, err := os.Stdout.Write(data); err != nil {
			return err
		}
	}
	fmt.Println("\nThe host does not run a DNS server. Advertise the profile DNS on the protected router/clients, disable bypass IPv6 routes, and keep Linux's default gateway upstream.")
	return errors.Join(failures...)
}
