package routing

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func custom(base Action, rules ...Rule) State {
	id := strings.Repeat("a", 32)
	return State{Version: 1, Selected: id, Profiles: []Profile{{ID: id, Name: "Home", Base: base, Rules: rules}}}
}

func TestValidationAndBuiltins(t *testing.T) {
	s := Default()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Active()
	if p.Base != VPN || len(p.Rules) != 0 {
		t.Fatal(p)
	}
	s.Selected = "direct"
	p, _ = s.Active()
	if p.Base != Direct {
		t.Fatal(p)
	}
	for _, mutate := range []func(*State){
		func(s *State) { s.Version = 2 },
		func(s *State) { s.Selected = "missing" },
		func(s *State) { s.Profiles[0].ID = "vpn" },
		func(s *State) { s.Profiles[0].Name = " VPN " },
		func(s *State) { s.Profiles[0].Name = "bad\nname" },
		func(s *State) { s.Profiles[0].Base = "wrong" },
		func(s *State) { s.Profiles[0].Rules = []Rule{{"192.168.1.5/16", Direct}} },
		func(s *State) { s.Profiles[0].Rules = []Rule{{"fe80::1%eth0", VPN}} },
		func(s *State) { s.Profiles[0].Rules = []Rule{{"::ffff:192.168.0.1", VPN}} },
		func(s *State) { s.Profiles[0].Rules = []Rule{{"1.1.1.1", VPN}, {"1.1.1.1/32", Direct}} },
		func(s *State) { s.Profiles = append(s.Profiles, s.Profiles[0]) },
		func(s *State) { s.Profiles[0].Rules = []Rule{{"0.0.0.0/0", "bad"}} },
	} {
		s := custom(VPN)
		mutate(&s)
		if s.Validate() == nil {
			t.Fatalf("accepted invalid state: %+v", s)
		}
	}
	s = custom(VPN, Rule{"1.1.1.1", Direct})
	if err := s.Validate(); err != nil || s.Profiles[0].Rules[0].Destination != "1.1.1.1/32" {
		t.Fatal(s, err)
	}
	clone := s.Clone()
	clone.Profiles[0].Rules[0].Action = VPN
	if s.Profiles[0].Rules[0].Action != Direct {
		t.Fatal("shared mutable rules")
	}
}

func TestCompiledRoutesMatchLongestPrefixPolicy(t *testing.T) {
	for _, base := range []Action{VPN, Direct} {
		state := custom(base,
			Rule{"192.168.0.0/16", Direct}, Rule{"192.168.50.0/24", VPN}, Rule{"192.168.50.1", Direct},
			Rule{"10.110.0.0/16", VPN}, Rule{"fd00::/8", Direct}, Rule{"fd00:1::/32", VPN}, Rule{"fd00:1::1", Direct})
		if err := state.Validate(); err != nil {
			t.Fatal(err)
		}
		p, _ := state.Active()
		for _, v6 := range []bool{false, true} {
			segments, err := p.Segments(v6)
			if err != nil {
				t.Fatal(err)
			}
			routes, err := VPNRoutes(p, v6, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i, segment := range segments {
				for j := 0; j < i; j++ {
					if segment.Prefix.Overlaps(segments[j].Prefix) {
						t.Fatal("overlapping policy leaves")
					}
				}
				addresses := []netip.Addr{segment.Prefix.Addr()}
				if next := segment.Prefix.Addr().Next(); segment.Prefix.Contains(next) {
					addresses = append(addresses, next)
				}
				for _, addr := range addresses {
					if p.ActionFor(addr) != segment.Action {
						t.Fatalf("wrong policy for %s", addr)
					}
					match := false
					for _, route := range routes {
						match = match || route.Contains(addr)
					}
					if match != (segment.Action == VPN) {
						t.Fatalf("wrong route for %s", addr)
					}
				}
			}
		}
	}
}

func TestBuiltinRoutesAndNAS(t *testing.T) {
	p, _ := Default().Active()
	got, err := VPNRoutes(p, false, nil)
	want := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	p.Base = Direct
	got, err = VPNRoutes(p, false, nil)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	p.Rules = []Rule{{"10.110.0.0/16", VPN}}
	got, err = VPNRoutes(p, false, nil)
	if err != nil || !reflect.DeepEqual(got, []netip.Prefix{netip.MustParsePrefix("10.110.0.0/16")}) {
		t.Fatal(got, err)
	}
}

func TestExistingRoutesCannotDefeatVPNSelection(t *testing.T) {
	p := Profile{Base: Direct, Rules: []Rule{{"192.168.0.0/16", VPN}, {"192.168.1.0/24", Direct}}}
	lan := netip.MustParsePrefix("192.168.50.0/24")
	direct := netip.MustParsePrefix("192.168.1.0/24")
	routes, err := VPNRoutes(p, false, []ExistingRoute{{lan, 281}, {direct, 0}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range routes {
		found = found || r == lan
		if r.Contains(direct.Addr()) {
			t.Fatal("direct destination received a VPN route")
		}
	}
	if !found {
		t.Fatal("missing override for existing connected route")
	}
	if _, err := VPNRoutes(p, false, []ExistingRoute{{lan, 0}}); err == nil {
		t.Fatal("ambiguous equal-cost route accepted")
	}
	p = Profile{Base: Direct, Rules: []Rule{{lan.String(), VPN}}}
	if _, err := VPNRoutes(p, false, []ExistingRoute{{lan, 0}}); err == nil {
		t.Fatal("equal-prefix conflict accepted")
	}
}

func FuzzRoutePolicy(f *testing.F) {
	f.Add(uint32(0xc0a83201), uint8(16), true)
	f.Fuzz(func(t *testing.T, address uint32, bits uint8, vpn bool) {
		ip := netip.AddrFrom4([4]byte{byte(address >> 24), byte(address >> 16), byte(address >> 8), byte(address)})
		prefix := netip.PrefixFrom(ip, int(bits%33)).Masked()
		base, action := Direct, VPN
		if vpn {
			base, action = VPN, Direct
		}
		p := Profile{Base: base, Rules: []Rule{{prefix.String(), action}}}
		routes, err := VPNRoutes(p, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range []netip.Addr{ip, ip.Next(), netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("255.255.255.255")} {
			if !candidate.IsValid() {
				continue
			}
			match := false
			for _, route := range routes {
				match = match || route.Contains(candidate)
			}
			if match != (p.ActionFor(candidate) == VPN) {
				t.Fatal("route/policy mismatch")
			}
		}
	})
}
