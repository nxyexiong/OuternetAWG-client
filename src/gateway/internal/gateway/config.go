package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

const (
	DataDirectory    = "/var/lib/outernetawg-gateway"
	RuntimeDirectory = "/run/outernetawg-gateway"
	SocketPath       = RuntimeDirectory + "/control.sock"
	MaxConfigSize    = 32 * 1024
	MaxMessageSize   = 1024 * 1024
)

type Config struct {
	IngressInterface string   `json:"ingress_interface"`
	TunnelInterface  string   `json:"tunnel_interface"`
	ProtectedSources []string `json:"protected_sources"`
}

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,14}$`)

func DecodeConfig(data []byte) (Config, error) {
	var c Config
	if len(data) > MaxConfigSize {
		return c, errors.New("gateway settings exceed 32 KiB")
	}
	if err := decodeJSON(data, &c); err != nil {
		return c, fmt.Errorf("invalid gateway settings: %w", err)
	}
	if c.TunnelInterface == "" {
		c.TunnelInterface = "oawg0"
	}
	return c, c.Validate()
}

func decodeJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func (c Config) Validate() error {
	for _, name := range []string{c.IngressInterface, c.TunnelInterface} {
		if !interfaceName.MatchString(name) || name == "lo" {
			return errors.New("interface names must be 1-15 ASCII letters, digits, underscores or hyphens, and not lo")
		}
	}
	if c.IngressInterface == c.TunnelInterface {
		return errors.New("ingress and tunnel interfaces must differ")
	}
	if len(c.ProtectedSources) == 0 || len(c.ProtectedSources) > 64 {
		return errors.New("configure 1-64 protected IPv4 source CIDRs")
	}
	prefixes := make([]netip.Prefix, 0, len(c.ProtectedSources))
	for _, text := range c.ProtectedSources {
		p, err := netip.ParsePrefix(text)
		if err != nil || !p.Addr().Is4() || !p.Addr().IsGlobalUnicast() || p.Bits() < 8 || p != p.Masked() {
			return fmt.Errorf("invalid protected source CIDR %q; use a canonical unicast IPv4 /8 through /32", text)
		}
		for _, other := range prefixes {
			if p.Overlaps(other) {
				return errors.New("protected source CIDRs must not overlap or repeat")
			}
		}
		prefixes = append(prefixes, p)
	}
	return nil
}

func (c Config) ValidateProfile(p *profile.Profile) error {
	if len(p.Addresses) != 1 || !p.Addresses[0].Addr().Is4() || p.Addresses[0].Bits() != 32 {
		return errors.New("the gateway currently requires one IPv4 /32 tunnel address and no IPv6 address")
	}
	if p.PersistentKeepalive() < 1 || p.PersistentKeepalive() > 120 {
		return errors.New("the gateway requires PersistentKeepalive between 1 and 120 seconds; portal profiles use 25")
	}
	for _, source := range c.ProtectedSources {
		if netip.MustParsePrefix(source).Contains(p.Addresses[0].Addr()) {
			return errors.New("the VPN address overlaps protected LAN sources")
		}
	}
	return nil
}
