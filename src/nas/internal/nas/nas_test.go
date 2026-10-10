package nas

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/nxyexiong/OuternetAWG-client/shared/awg"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testProfile(t *testing.T) *profile.Profile {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	p, err := profile.Parse("NAS", "[Interface]\nPrivateKey = "+key+"\nAddress = 10.110.0.2/32\nDNS = 1.1.1.1\n"+
		"[Peer]\nPublicKey = "+key+"\nEndpoint = 192.0.2.1:51820\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 25\n")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfiguration(t *testing.T) {
	c := testConfig(t)
	if c.Subnet.String() != "10.110.0.0/16" || c.Interface != "oawgnas0" {
		t.Fatal("wrong defaults")
	}
	if err := c.ValidateProfile(testProfile(t)); err != nil {
		t.Fatal(err)
	}
	for _, subnet := range []string{"::/64", "10.110.0.1/16", "0.0.0.0/0", "10.110.0.2/32", "127.0.0.0/8", "224.0.0.0/8", "169.254.0.0/16"} {
		c.Subnet = netip.MustParsePrefix(subnet)
		if c.Validate() == nil {
			t.Fatal("accepted invalid subnet", subnet)
		}
	}
	c = testConfig(t)
	for _, name := range []string{"lo", "bad;command", "very-long-tunnel-name", "../eth0", ""} {
		c.Interface = name
		if c.Validate() == nil {
			t.Fatal("accepted invalid name", name)
		}
	}
	c = testConfig(t)
	for _, address := range []string{"10.111.0.2/32", "10.110.0.2/24", "10.110.0.0/32", "10.110.255.255/32", "fd00::2/128"} {
		p := testProfile(t)
		p.Addresses = []netip.Prefix{netip.MustParsePrefix(address)}
		if c.ValidateProfile(p) == nil {
			t.Fatal("accepted invalid address", address)
		}
	}
}

func TestNetworkRefusesConflicts(t *testing.T) {
	c := testConfig(t)
	for _, route := range []string{
		`[{"dst":"10.110.0.0/16"}]`, `[{"dst":"10.0.0.0/8","table":100}]`,
		`[{"dst":"10.110.0.2","table":"local"}]`, `[{"dst":"0.0.0.0/1"}]`, `invalid`, `[{}]`,
	} {
		err := checkNetwork(context.Background(), c, func(_ context.Context, args ...string) ([]byte, error) {
			if args[1] == "link" {
				return []byte("[]"), nil
			}
			return []byte(route), nil
		})
		if err == nil {
			t.Fatal("accepted conflict or malformed route", route)
		}
	}
	err := checkNetwork(context.Background(), c, func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "link" {
			return []byte(`[{"ifname":"oawgnas0"}]`), nil
		}
		t.Fatal("should stop after interface conflict")
		return nil, nil
	})
	if err == nil {
		t.Fatal("existing interface accepted")
	}
}

func TestNetworkAddsOnlyEndpointAddressAndSubnet(t *testing.T) {
	c, p := testConfig(t), testProfile(t)
	err := checkNetwork(context.Background(), c, func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "link" {
			return []byte(`[{"ifname":"eth0"}]`), nil
		}
		return []byte(`[{"dst":"default"},{"dst":"192.168.0.0/24"},{"dst":"172.17.0.0/16"}]`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	err = configureNetwork(context.Background(), c, p, func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, strings.Join(args, " "))
		return nil, nil
	})
	expected := "link set dev oawgnas0 addrgenmode none\n-4 address add 10.110.0.2/32 dev oawgnas0\nlink set dev oawgnas0 mtu 1280 up\n-4 route add 10.110.0.0/16 dev oawgnas0 src 10.110.0.2"
	if err != nil || strings.Join(commands, "\n") != expected {
		t.Fatal("unexpected network mutations", commands, err)
	}
}

func packet(source, destination string) []byte {
	p := make([]byte, 28)
	p[0], p[8], p[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	src, dst := netip.MustParseAddr(source).As4(), netip.MustParseAddr(destination).As4()
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	return p
}

type fakeTUN struct {
	tun.Device
	packets [][]byte
	written [][]byte
}

func (f *fakeTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(f.packets) == 0 {
		return 0, io.EOF
	}
	n := min(len(bufs), len(f.packets))
	for i, p := range f.packets[:n] {
		sizes[i] = copy(bufs[i][offset:], p)
	}
	f.packets = f.packets[n:]
	return n, nil
}

func (f *fakeTUN) Write(bufs [][]byte, offset int) (int, error) {
	for _, buf := range bufs {
		f.written = append(f.written, bytes.Clone(buf[offset:]))
	}
	return len(bufs), nil
}

func TestEndpointPacketIsolationAndBatchCompaction(t *testing.T) {
	fake := &fakeTUN{}
	filter := &endpointTUN{Device: fake, subnet: netip.MustParsePrefix("10.110.0.0/16"), address: netip.MustParseAddr("10.110.0.2")}
	accepted := packet("10.110.0.3", "10.110.0.2")
	options := bytes.Clone(accepted)
	options[0] = 0x46
	if filter.accepts(options, true) {
		t.Fatal("IPv4 options accepted")
	}
	for _, rejected := range [][]byte{
		packet("10.110.0.3", "192.168.0.10"), packet("10.110.0.3", "1.1.1.1"),
		packet("192.168.0.10", "10.110.0.2"), packet("10.110.0.0", "10.110.0.2"),
		packet("10.110.255.255", "10.110.0.2"), packet("10.110.0.2", "10.110.0.2"),
		{0x60, 0, 0, 0}, accepted[:10],
	} {
		if filter.accepts(rejected, true) {
			t.Fatal("unsafe inbound packet accepted")
		}
	}
	if n, err := filter.Write([][]byte{append(make([]byte, 16), accepted...), append(make([]byte, 16), packet("10.110.0.3", "192.168.0.10")...)}, 16); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if len(fake.written) != 1 || !bytes.Equal(fake.written[0], accepted) {
		t.Fatal("inbound filter did not isolate the NAS address")
	}
	outbound := packet("10.110.0.2", "10.110.0.3")
	fake.packets = [][]byte{packet("192.168.0.10", "10.110.0.3"), outbound, packet("10.110.0.2", "1.1.1.1"), outbound}
	bufs, sizes := [][]byte{make([]byte, 128), make([]byte, 128), make([]byte, 128), make([]byte, 128)}, make([]int, 4)
	n, err := filter.Read(bufs, sizes, 16)
	if n != 2 || err != nil || !bytes.Equal(bufs[0][16:16+sizes[0]], outbound) || !bytes.Equal(bufs[1][16:16+sizes[1]], outbound) || filter.dropped.Load() != 3 {
		t.Fatal("outbound batch filter failed", n, err, filter.dropped.Load())
	}
}

func TestHandshakeAndStatusThresholds(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		handshake time.Time
		started   time.Time
		state     string
		failed    bool
	}{
		{time.Time{}, now.Add(-44 * time.Second), "waiting", false},
		{time.Time{}, now.Add(-45 * time.Second), "reconnecting", true},
		{now.Add(-179 * time.Second), now.Add(-time.Hour), "connected", false},
		{now.Add(-180 * time.Second), now.Add(-time.Hour), "reconnecting", true},
		{now.Add(6 * time.Second), now.Add(-time.Hour), "reconnecting", true},
	} {
		state, err := connectionState(awg.Traffic{Handshake: test.handshake}, test.started, now)
		if state != test.state || (err != nil) != test.failed {
			t.Fatal("incorrect handshake threshold", test, state, err)
		}
	}
	s := Status{State: "connected", UpdatedAt: now, Traffic: awg.Traffic{Handshake: now}}
	if s.Check(now, true) != nil || s.Check(now.Add(11*time.Second), true) == nil {
		t.Fatal("status freshness not enforced")
	}
	for failures, expected := range []time.Duration{5, 5, 10, 20, 40, 60, 60, 60} {
		if retryDelay(failures) != expected*time.Second {
			t.Fatal("incorrect backoff", failures)
		}
	}
}

type fakeSession struct {
	done   chan struct{}
	closed bool
}

func (s *fakeSession) Stats() (awg.Traffic, error) { return awg.Traffic{Handshake: time.Now()}, nil }
func (s *fakeSession) Done() <-chan struct{}       { return s.done }
func (s *fakeSession) Dropped() uint64             { return 0 }
func (s *fakeSession) Close() error                { s.closed = true; return nil }

func TestClientShutdownAndStatusFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		session := &fakeSession{done: make(chan struct{})}
		var states []string
		client := Client{Config: testConfig(t), Profile: testProfile(t),
			Open: func(context.Context, Config, *profile.Profile) (Session, error) {
				if !fail {
					cancel()
				}
				return session, nil
			},
			Publish: func(s Status) error {
				states = append(states, s.State)
				if fail && s.State == "connected" {
					return errors.New("runtime disk failure")
				}
				return nil
			},
		}
		err := client.Run(ctx)
		cancel()
		if (err != nil) != fail || !session.closed || states[len(states)-1] != "stopped" {
			t.Fatal("shutdown/failed status did not close tunnel", err, states)
		}
	}
}

func FuzzEndpointFilter(f *testing.F) {
	f.Add(packet("10.110.0.3", "10.110.0.2"), true)
	f.Add([]byte{0x60}, false)
	filter := &endpointTUN{subnet: netip.MustParsePrefix("10.110.0.0/16"), address: netip.MustParseAddr("10.110.0.2")}
	f.Fuzz(func(t *testing.T, packet []byte, inbound bool) {
		filter.accepts(packet, inbound)
	})
}
