package routing

import (
	"errors"
	"net/netip"
	"sort"
)

type ExistingRoute struct {
	Prefix netip.Prefix
	Cost   uint64
}

func VPNRoutes(p Profile, ipv6 bool, existing []ExistingRoute) ([]netip.Prefix, error) {
	segments, err := p.Segments(ipv6)
	if err != nil {
		return nil, err
	}
	prefixes := map[netip.Prefix]bool{}
	for _, segment := range segments {
		if segment.Action != VPN {
			continue
		}
		if segment.Prefix.Bits() == 0 {
			a, b := Children(segment.Prefix)
			prefixes[a], prefixes[b] = true, true
		} else {
			prefixes[segment.Prefix] = true
		}
		for _, route := range existing {
			if route.Prefix.Bits() == 0 || route.Prefix.Bits() < segment.Prefix.Bits() || !segment.Prefix.Contains(route.Prefix.Addr()) {
				continue
			}
			if route.Cost == 0 {
				return nil, errors.New("an existing zero-cost route conflicts with VPN routing; adjust that route before connecting")
			}
			// Mirror more-specific routes on our adapter, never mutate another adapter.
			prefixes[route.Prefix] = true
		}
	}
	if len(prefixes) > 8192 {
		return nil, errors.New("routing policy expands beyond 8192 routes; simplify its exceptions")
	}
	result := make([]netip.Prefix, 0, len(prefixes))
	for prefix := range prefixes {
		result = append(result, prefix)
	}
	sort.Slice(result, func(i, j int) bool {
		if c := result[i].Addr().Compare(result[j].Addr()); c != 0 {
			return c < 0
		}
		return result[i].Bits() < result[j].Bits()
	})
	return result, nil
}
