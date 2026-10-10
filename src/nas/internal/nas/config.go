package nas

import (
	"errors"
	"net/netip"
	"regexp"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

const RuntimeDirectory = "/run/outernetawg"

type Config struct {
	ProfileFile string
	Interface   string
	Subnet      netip.Prefix
}

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`)

func LoadConfig(getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if text := getenv(key); text != "" {
			return text
		}
		return fallback
	}
	subnet, err := netip.ParsePrefix(value("OUTERNETAWG_VPN_SUBNET", "10.110.0.0/16"))
	if err != nil {
		return Config{}, errors.New("OUTERNETAWG_VPN_SUBNET must be an IPv4 network in CIDR notation")
	}
	c := Config{
		ProfileFile: value("OUTERNETAWG_PROFILE_FILE", "/run/secrets/outernetawg-profile"),
		Interface:   value("OUTERNETAWG_TUN_NAME", "oawgnas0"),
		Subnet:      subnet,
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !interfaceName.MatchString(c.Interface) || c.Interface == "lo" {
		return errors.New("OUTERNETAWG_TUN_NAME must be 1-15 ASCII letters, digits, underscores or hyphens, and not lo")
	}
	if !c.Subnet.IsValid() || !c.Subnet.Addr().Is4() || c.Subnet != c.Subnet.Masked() ||
		c.Subnet.Bits() < 8 || c.Subnet.Bits() > 30 {
		return errors.New("OUTERNETAWG_VPN_SUBNET must be a canonical IPv4 /8 through /30")
	}
	for _, excluded := range []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
		if c.Subnet.Overlaps(netip.MustParsePrefix(excluded)) {
			return errors.New("VPN subnet overlaps reserved, loopback, link-local or multicast addresses")
		}
	}
	if c.ProfileFile == "" {
		return errors.New("a private profile file is required")
	}
	return nil
}

func (c Config) ValidateProfile(p *profile.Profile) error {
	if len(p.Addresses) != 1 || !p.Addresses[0].Addr().Is4() || p.Addresses[0].Bits() != 32 ||
		!c.Subnet.Contains(p.Addresses[0].Addr()) {
		return errors.New("NAS profile requires exactly one IPv4 /32 inside OUTERNETAWG_VPN_SUBNET")
	}
	if !hostAddress(c.Subnet, p.Addresses[0].Addr()) {
		return errors.New("NAS address must not be the subnet's network or broadcast address")
	}
	if p.PersistentKeepalive() < 1 || p.PersistentKeepalive() > 120 {
		return errors.New("NAS profile requires PersistentKeepalive from 1 through 120 seconds")
	}
	return nil
}

func hostAddress(subnet netip.Prefix, address netip.Addr) bool {
	if !address.Is4() || !subnet.Contains(address) || address == subnet.Addr() {
		return false
	}
	bytes := address.As4()
	value := uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
	mask := uint32(1)<<(32-subnet.Bits()) - 1
	return value&mask != mask
}
