package firewall

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
	"unsafe"

	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
	"golang.org/x/sys/windows"
)

type capturedFilter struct {
	prefix netip.Prefix
	layer  windows.GUID
	weight uint64
	permit bool
	match  wtFwpMatchType
	luid   uint64
}

func readNative[T any](t *testing.T, address uintptr) T {
	t.Helper()
	var value T
	var read uintptr
	size := unsafe.Sizeof(value)
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&value)), size, &read); err != nil || read != size {
		t.Fatalf("read native filter structure: %v (%d/%d)", err, read, size)
	}
	return value
}

func capturePolicy(t *testing.T, policy routing.Profile) []capturedFilter {
	t.Helper()
	var filters []capturedFilter
	err := installPolicy(0, &baseObjects{}, 42, policy, func(_ uintptr, f *wtFwpmFilter0, _ uintptr, _ *uint64) error {
		conditions := unsafe.Slice(f.filterCondition, f.numFilterConditions)
		c := conditions[0].conditionValue
		var prefix netip.Prefix
		if c._type == cFWP_V4_ADDR_MASK {
			v := readNative[wtFwpV4AddrAndMask](t, c.value)
			var bytes [4]byte
			binary.BigEndian.PutUint32(bytes[:], v.addr)
			bits := 0
			for mask := v.mask; mask != 0; mask <<= 1 {
				bits++
			}
			prefix = netip.PrefixFrom(netip.AddrFrom4(bytes), bits)
		} else {
			v := readNative[wtFwpV6AddrAndMask](t, c.value)
			prefix = netip.PrefixFrom(netip.AddrFrom16(v.addr), int(v.prefixLength))
		}
		filter := capturedFilter{prefix: prefix, layer: f.layerKey, weight: readNative[uint64](t, f.weight.value), permit: f.action._type == cFWP_ACTION_PERMIT}
		if filter.permit {
			if len(conditions) != 2 || conditions[1].fieldKey != cFWPM_CONDITION_IP_LOCAL_INTERFACE {
				t.Fatal("unscoped permit")
			}
			filter.match = conditions[1].matchType
			filter.luid = readNative[uint64](t, conditions[1].conditionValue.value)
		} else if len(conditions) != 1 {
			t.Fatal("fallback block is not destination-wide")
		}
		filters = append(filters, filter)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return filters
}

func TestWFPPolicyMatchesLongestPrefixInBothDirections(t *testing.T) {
	for _, base := range []routing.Action{routing.VPN, routing.Direct} {
		p := routing.Profile{Base: base, Rules: []routing.Rule{
			{Destination: "192.168.0.0/16", Action: routing.Direct},
			{Destination: "192.168.50.0/24", Action: routing.VPN},
			{Destination: "fd00::/8", Action: routing.Direct},
			{Destination: "fd00:1::/32", Action: routing.VPN},
		}}
		filters := capturePolicy(t, p)
		for _, text := range []string{"192.168.0.1", "192.168.50.2", "1.1.1.1", "fd00::1", "fd00:1::1", "2606:4700::1111"} {
			ip := netip.MustParseAddr(text)
			layers := []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4}
			if ip.Is6() {
				layers = []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6}
			}
			for _, layer := range layers {
				for _, iface := range []uint64{42, 7} {
					var winner *capturedFilter
					for i := range filters {
						f := &filters[i]
						if f.layer != layer || !f.prefix.Contains(ip) {
							continue
						}
						if f.permit && (f.match == cFWP_MATCH_EQUAL && iface != f.luid || f.match == cFWP_MATCH_NOT_EQUAL && iface == f.luid) {
							continue
						}
						if winner == nil || f.weight > winner.weight {
							winner = f
						}
					}
					want := (p.ActionFor(ip) == routing.VPN) == (iface == 42)
					if winner == nil || winner.permit != want {
						t.Fatalf("wrong policy: %s on interface %d", ip, iface)
					}
				}
			}
		}
	}
}

func TestWFPDefaultOverrideAndErrors(t *testing.T) {
	filters := capturePolicy(t, routing.Profile{Base: routing.VPN, Rules: []routing.Rule{{Destination: "0.0.0.0/0", Action: routing.Direct}}})
	if len(filters) != 8 {
		t.Fatal("duplicate catch-all filters")
	}
	for _, f := range filters {
		if f.weight >= uint64(12)<<60 {
			t.Fatal("overrode essential traffic")
		}
		if f.prefix.Addr().Is4() && f.permit && f.match != cFWP_MATCH_NOT_EQUAL {
			t.Fatal("default override ignored")
		}
	}
	failed := errors.New("synthetic WFP rejection")
	if err := installPolicy(0, &baseObjects{}, 42, routing.Profile{Base: routing.VPN},
		func(uintptr, *wtFwpmFilter0, uintptr, *uint64) error { return failed }); !errors.Is(err, failed) {
		t.Fatal("filter failure hidden", err)
	}
}
