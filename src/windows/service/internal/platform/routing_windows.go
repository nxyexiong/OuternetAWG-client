package platform

import (
	"fmt"
	"net/netip"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func prepareRouting(p *profile.Profile, policy routing.Profile, endpoint netip.Addr) ([]netip.Prefix, error) {
	ipv6 := false
	for _, address := range p.Addresses {
		ipv6 = ipv6 || address.Addr().Is6()
	}
	for _, r := range policy.Rules {
		prefix, err := routing.ParseDestination(r.Destination)
		if err != nil {
			return nil, err
		}
		if r.Action == routing.VPN && prefix.Addr().Is6() && !ipv6 {
			return nil, fmt.Errorf("routing rule %s requires an IPv6-enabled VPN configuration", prefix)
		}
	}
	if policy.Base == routing.VPN {
		for _, dns := range p.DNS {
			if policy.ActionFor(dns) != routing.VPN || dns.Is6() && !ipv6 {
				return nil, fmt.Errorf("VPN DNS server %s must remain reachable through the VPN; adjust the routing profile", dns)
			}
		}
	}
	var result []netip.Prefix
	local := map[netip.Addr]bool{endpoint: true}
	for _, address := range p.Addresses {
		local[address.Addr()] = true
	}
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		if family == windows.AF_INET6 && !ipv6 {
			continue
		}
		rows, err := winipcfg.GetIPForwardTable2(family)
		if err != nil {
			return nil, fmt.Errorf("inspect existing routes: %w", err)
		}
		var existing []routing.ExistingRoute
		for _, row := range rows {
			prefix := row.DestinationPrefix.Prefix()
			if prefix.IsValid() && prefix.Bits() == prefix.Addr().BitLen() && row.Protocol == winipcfg.RouteProtocolLocal {
				local[prefix.Addr()] = true
			}
			if !prefix.IsValid() || row.ValidLifetime == 0 || prefix.Addr().IsLoopback() ||
				prefix.Addr().IsMulticast() || prefix.Addr().IsLinkLocalUnicast() ||
				prefix.Bits() == prefix.Addr().BitLen() && (row.Protocol == winipcfg.RouteProtocolLocal || prefix.Addr() == endpoint) {
				continue
			}
			iface, err := row.InterfaceLUID.Interface()
			if err != nil {
				return nil, fmt.Errorf("inspect route adapter: %w", err)
			}
			if iface.OperStatus != winipcfg.IfOperStatusUp {
				continue
			}
			ip, err := row.InterfaceLUID.IPInterface(family)
			if err != nil {
				return nil, fmt.Errorf("inspect route metric: %w", err)
			}
			existing = append(existing, routing.ExistingRoute{Prefix: prefix, Cost: uint64(row.Metric) + uint64(ip.Metric)})
		}
		routes, err := routing.VPNRoutes(policy, family == windows.AF_INET6, existing)
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			if route.Bits() == route.Addr().BitLen() && local[route.Addr()] {
				continue
			}

			result = append(result, route)
		}
	}
	return result, nil
}

// A profile's address mask must not create implicit routes that bypass its routing policy.
func hostAddresses(addresses []netip.Prefix) []netip.Prefix {
	result := make([]netip.Prefix, len(addresses))
	for i, address := range addresses {
		result[i] = netip.PrefixFrom(address.Addr(), address.Addr().BitLen())
	}
	return result
}
