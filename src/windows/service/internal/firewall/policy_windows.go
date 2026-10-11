package firewall

import (
	"encoding/binary"
	"net/netip"
	"runtime"
	"unsafe"

	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
	"golang.org/x/sys/windows"
)

// The ABI and essential-traffic helpers are from wireguard/windows v1.0.1.
// Policy filters share its transaction/sublayer; separate Windows Firewall
// allow rules cannot override the helper's catch-all block reliably.
type filterAdder func(uintptr, *wtFwpmFilter0, uintptr, *uint64) error

func installPolicy(session uintptr, objects *baseObjects, luid uint64, policy routing.Profile, add filterAdder) error {
	rules := []routing.Rule{{Destination: "0.0.0.0/0", Action: policy.Base}, {Destination: "::/0", Action: policy.Base}}
	for _, r := range policy.Rules {
		prefix, err := routing.ParseDestination(r.Destination)
		if err != nil {
			return err
		}
		if prefix.Bits() == 0 {
			index := 0
			if prefix.Addr().Is6() {
				index = 1
			}
			rules[index] = r
		} else {
			rules = append(rules, r)
		}
	}
	for _, rule := range rules {
		prefix, err := routing.ParseDestination(rule.Destination)
		if err != nil {
			return err
		}
		if err = installDestination(session, objects, luid, prefix, rule.Action, add); err != nil {
			return err
		}
	}
	return nil
}

func policyWeight(bits int, permit bool) uint64 {
	// Below DHCP/NDP (range 12), loopback (13), DNS (14/15), service (15).
	weight := uint64(11)<<60 | uint64(bits*2)
	if permit {
		weight++
	}
	return weight
}

func installDestination(session uintptr, objects *baseObjects, luid uint64, prefix netip.Prefix, action routing.Action, add filterAdder) error {
	display, err := createWtFwpmDisplayData0("OuternetAWG "+string(action)+" "+prefix.String(), "")
	if err != nil {
		return err
	}
	var v4 wtFwpV4AddrAndMask
	var v6 wtFwpV6AddrAndMask
	address := wtFwpConditionValue0{}
	layers := []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4}
	if prefix.Addr().Is4() {
		v4 = wtFwpV4AddrAndMask{binary.BigEndian.Uint32(prefix.Addr().AsSlice()), ^uint32(0) << (32 - prefix.Bits())}
		address = wtFwpConditionValue0{_type: cFWP_V4_ADDR_MASK, value: uintptr(unsafe.Pointer(&v4))}
	} else {
		v6 = wtFwpV6AddrAndMask{prefix.Addr().As16(), uint8(prefix.Bits())}
		address = wtFwpConditionValue0{_type: cFWP_V6_ADDR_MASK, value: uintptr(unsafe.Pointer(&v6))}
		layers = []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6}
	}
	match := cFWP_MATCH_EQUAL
	if action == routing.Direct {
		match = cFWP_MATCH_NOT_EQUAL
	}
	conditions := []wtFwpmFilterCondition0{
		{fieldKey: cFWPM_CONDITION_IP_REMOTE_ADDRESS, matchType: cFWP_MATCH_EQUAL, conditionValue: address},
		{fieldKey: cFWPM_CONDITION_IP_LOCAL_INTERFACE, matchType: match,
			conditionValue: wtFwpConditionValue0{_type: cFWP_UINT64, value: uintptr(unsafe.Pointer(&luid))}},
	}
	for _, layer := range layers {
		for _, permit := range []bool{false, true} {
			weight := policyWeight(prefix.Bits(), permit)
			filter := wtFwpmFilter0{
				providerKey: &objects.provider, subLayerKey: objects.filters,
				displayData: *display, layerKey: layer,
				weight:              wtFwpValue0{_type: cFWP_UINT64, value: uintptr(unsafe.Pointer(&weight))},
				numFilterConditions: 1, filterCondition: &conditions[0],
				action: wtFwpmAction0{_type: cFWP_ACTION_BLOCK},
			}
			if permit {
				filter.numFilterConditions = 2
				filter.action._type = cFWP_ACTION_PERMIT
			}
			var id uint64
			if err := add(session, &filter, 0, &id); err != nil {
				return wrapErr(err)
			}
			runtime.KeepAlive(weight)
		}
	}
	runtime.KeepAlive(v4)
	runtime.KeepAlive(v6)
	runtime.KeepAlive(luid)
	runtime.KeepAlive(conditions)
	return nil
}
