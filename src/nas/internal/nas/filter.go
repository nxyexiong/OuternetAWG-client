package nas

import (
	"encoding/binary"
	"net/netip"
	"sync/atomic"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

// Filter before injection into the host stack, even when Docker has enabled
// forwarding. Only this NAS's VPN address is reachable, never its LAN addresses.
type endpointTUN struct {
	tun.Device
	subnet  netip.Prefix
	address netip.Addr
	dropped atomic.Uint64
}

func (t *endpointTUN) accepts(packet []byte, inbound bool) bool {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return false
	}
	// IP source-route options could change the destination after this check.
	if packet[0]&15 != 5 || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) {
		return false
	}
	source := netip.AddrFrom4([4]byte(packet[12:16]))
	destination := netip.AddrFrom4([4]byte(packet[16:20]))
	if inbound {
		return destination == t.address && source != t.address && hostAddress(t.subnet, source)
	}
	return source == t.address && destination != t.address && hostAddress(t.subnet, destination)
}

func (t *endpointTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for {
		n, err := t.Device.Read(bufs, sizes, offset)
		count := 0
		for i := 0; i < n; i++ {
			packet := bufs[i][offset : offset+sizes[i]]
			if !t.accepts(packet, false) {
				t.dropped.Add(1)
				continue
			}
			if i != count {
				copy(bufs[count][offset:], packet)
			}
			sizes[count] = sizes[i]
			count++
		}
		if count > 0 || err != nil {
			return count, err
		}
	}
}

func (t *endpointTUN) Write(bufs [][]byte, offset int) (int, error) {
	allowed := make([][]byte, 0, len(bufs))
	for _, buf := range bufs {
		if t.accepts(buf[offset:], true) {
			allowed = append(allowed, buf)
		} else {
			t.dropped.Add(1)
		}
	}
	if len(allowed) > 0 {
		if _, err := t.Device.Write(allowed, offset); err != nil {
			return 0, err
		}
	}
	return len(bufs), nil
}
