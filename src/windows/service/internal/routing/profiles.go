package routing

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
)

type Action string

const (
	VPN         Action = "vpn"
	Direct      Action = "direct"
	MaxProfiles        = 32
	MaxRules           = 64
)

type Rule struct {
	Destination string `json:"destination"`
	Action      Action `json:"action"`
}

type Profile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Base  Action `json:"base"`
	Rules []Rule `json:"rules"`
}

type State struct {
	Version  int       `json:"version"`
	Selected string    `json:"selected"`
	Profiles []Profile `json:"profiles"`
}

var customID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func Default() State { return State{Version: 1, Selected: "vpn", Profiles: []Profile{}} }

func (s State) Clone() State {
	result := s
	result.Profiles = make([]Profile, len(s.Profiles))
	for i, p := range s.Profiles {
		result.Profiles[i] = p
		result.Profiles[i].Rules = append([]Rule{}, p.Rules...)
	}
	return result
}

func (s State) Active() (Profile, error) {
	switch s.Selected {
	case "vpn":
		return Profile{ID: "vpn", Name: "VPN", Base: VPN, Rules: []Rule{}}, nil
	case "direct":
		return Profile{ID: "direct", Name: "Direct", Base: Direct, Rules: []Rule{}}, nil
	}
	for _, p := range s.Profiles {
		if p.ID == s.Selected {
			return p, nil
		}
	}
	return Profile{}, errors.New("selected routing profile does not exist")
}

func ParseDestination(text string) (netip.Prefix, error) {
	if ip, err := netip.ParseAddr(text); err == nil && ip.Zone() == "" && !ip.Is4In6() {
		return netip.PrefixFrom(ip, ip.BitLen()), nil
	}
	p, err := netip.ParsePrefix(text)
	if err != nil || p.Addr().Is4In6() || p != p.Masked() {
		return netip.Prefix{}, errors.New("use an IP address or canonical CIDR (for example 192.168.0.0/16)")
	}
	return p, nil
}

func (s *State) Validate() error {
	if s.Version != 1 || len(s.Profiles) > MaxProfiles {
		return errors.New("unsupported routing settings version or too many profiles (maximum 32)")
	}
	ids, names := map[string]bool{}, map[string]bool{"vpn": true, "direct": true}
	for i := range s.Profiles {
		p := &s.Profiles[i]
		if !customID.MatchString(p.ID) || ids[p.ID] {
			return errors.New("routing profile IDs must be unique, lowercase 32-digit hexadecimal identifiers")
		}
		p.Name = strings.TrimSpace(p.Name)
		name := strings.ToLower(p.Name)
		if len([]rune(p.Name)) < 1 || len([]rune(p.Name)) > 64 || names[name] ||
			strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
			return errors.New("routing profile names must be unique, 1-64 characters, and not VPN or Direct")
		}
		ids[p.ID], names[name] = true, true
		if p.Base != VPN && p.Base != Direct || len(p.Rules) > MaxRules {
			return errors.New("choose VPN or Direct as the base and use at most 64 rules")
		}
		destinations := map[netip.Prefix]bool{}
		for j := range p.Rules {
			r := &p.Rules[j]
			prefix, err := ParseDestination(strings.TrimSpace(r.Destination))
			if err != nil {
				return fmt.Errorf("profile %q, rule %d: %w", p.Name, j+1, err)
			}
			if r.Action != VPN && r.Action != Direct || destinations[prefix] {
				return fmt.Errorf("profile %q, rule %d: duplicate destination or invalid action", p.Name, j+1)
			}
			r.Destination = prefix.String()
			destinations[prefix] = true
		}
		if p.Rules == nil {
			p.Rules = []Rule{}
		}
	}
	if s.Profiles == nil {
		s.Profiles = []Profile{}
	}
	_, err := s.Active()
	return err
}

func (p Profile) ActionFor(ip netip.Addr) Action {
	action, bits := p.Base, -1
	for _, rule := range p.Rules {
		prefix, err := ParseDestination(rule.Destination)
		if err == nil && prefix.Contains(ip) && prefix.Bits() > bits {
			action, bits = rule.Action, prefix.Bits()
		}
	}
	return action
}

type Segment struct {
	Prefix netip.Prefix
	Action Action
}

// Disjoint segments prevent a broad VPN route from swallowing a Direct exception.
func (p Profile) Segments(ipv6 bool) ([]Segment, error) {
	root := netip.MustParsePrefix("0.0.0.0/0")
	if ipv6 {
		root = netip.MustParsePrefix("::/0")
	}
	rules := make([]Segment, 0, len(p.Rules))
	for _, r := range p.Rules {
		prefix, err := ParseDestination(r.Destination)
		if err != nil {
			return nil, err
		}
		if prefix.Addr().Is6() == ipv6 {
			rules = append(rules, Segment{prefix, r.Action})
		}
	}
	var split func(netip.Prefix, Action) []Segment
	split = func(prefix netip.Prefix, inherited Action) []Segment {
		deeper := false
		for _, r := range rules {
			if r.Prefix == prefix {
				inherited = r.Action
			}
			if r.Prefix.Bits() > prefix.Bits() && prefix.Contains(r.Prefix.Addr()) {
				deeper = true
			}
		}
		if !deeper {
			return []Segment{{prefix, inherited}}
		}
		left, right := Children(prefix)
		a, b := split(left, inherited), split(right, inherited)
		if len(a) == 1 && len(b) == 1 && a[0].Action == b[0].Action {
			return []Segment{{prefix, a[0].Action}}
		}
		return append(a, b...)
	}
	return split(root, p.Base), nil
}

func Children(p netip.Prefix) (netip.Prefix, netip.Prefix) {
	bits := p.Bits()
	if p.Addr().Is4() {
		bytes := p.Addr().As4()
		bytes[bits/8] |= 1 << (7 - bits%8)
		return netip.PrefixFrom(p.Addr(), bits+1), netip.PrefixFrom(netip.AddrFrom4(bytes), bits+1)
	}
	bytes := p.Addr().As16()
	bytes[bits/8] |= 1 << (7 - bits%8)
	return netip.PrefixFrom(p.Addr(), bits+1), netip.PrefixFrom(netip.AddrFrom16(bytes), bits+1)
}
