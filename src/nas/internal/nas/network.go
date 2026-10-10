package nas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

type runIP func(context.Context, ...string) ([]byte, error)

type boundedOutput struct{ bytes.Buffer }

// Do not embed a ReaderFrom implementation: exec must use the bounded Write.
type outputWriter struct{ output *boundedOutput }

func (w outputWriter) Write(p []byte) (int, error) {
	if w.output.Len()+len(p) > 4*1024*1024 {
		return 0, errors.New("iproute2 output exceeded 4 MiB")
	}
	return w.output.Buffer.Write(p)
}

func systemIP(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ip", args...)
	var stdout, stderr boundedOutput
	command.Stdout, command.Stderr = outputWriter{&stdout}, outputWriter{&stderr}
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 2048 {
			message = message[:2048]
		}
		return nil, fmt.Errorf("iproute2 failed: %w: %s", err, message)
	}
	return stdout.Bytes(), nil
}

func checkNetwork(ctx context.Context, c Config, run runIP) error {
	data, err := run(ctx, "-j", "link", "show")
	if err != nil {
		return err
	}
	var links []struct {
		Name string `json:"ifname"`
	}
	if err := json.Unmarshal(data, &links); err != nil {
		return fmt.Errorf("decode interface list: %w", err)
	}
	for _, link := range links {
		if link.Name == c.Interface {
			return errors.New("NAS tunnel interface already exists; refusing to take it over")
		}
	}
	data, err = run(ctx, "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return err
	}
	var routes []struct {
		Destination string `json:"dst"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return fmt.Errorf("decode IPv4 routes: %w", err)
	}
	for _, route := range routes {
		if route.Destination == "default" || route.Destination == "0.0.0.0/0" {
			continue
		}
		destination := route.Destination
		if !strings.Contains(destination, "/") {
			destination += "/32"
		}
		prefix, err := netip.ParsePrefix(destination)
		if err != nil || !prefix.Addr().Is4() {
			return errors.New("iproute2 returned an invalid IPv4 route")
		}
		if prefix.Overlaps(c.Subnet) {
			return fmt.Errorf("VPN subnet overlaps existing route %s; refusing to change NAS routing", prefix)
		}
	}
	return nil
}

func configureNetwork(ctx context.Context, c Config, p *profile.Profile, run runIP) error {
	for _, args := range [][]string{
		{"link", "set", "dev", c.Interface, "addrgenmode", "none"},
		{"-4", "address", "add", p.Addresses[0].String(), "dev", c.Interface},
		{"link", "set", "dev", c.Interface, "mtu", strconv.Itoa(p.MTU), "up"},
		{"-4", "route", "add", c.Subnet.String(), "dev", c.Interface, "src", p.Addresses[0].Addr().String()},
	} {
		if _, err := run(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}
