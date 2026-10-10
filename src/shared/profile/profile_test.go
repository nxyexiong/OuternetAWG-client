package profile

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

func config() string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.110.0.2/32\nDNS = 1.1.1.1\nMTU = 1280\n" +
		"Jc = 4\nJmin = 40\nJmax = 80\nS1 = 16\nS2 = 16\nS3 = 16\nS4 = 16\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\nHeaderProtectionKey = " + key +
		"\n[Peer]\nPublicKey = " + key + "\nEndpoint = node.example.com:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n"
}

func TestManagementConfigAndSplitDefaults(t *testing.T) {
	p, err := Parse("Test node", config())
	if err != nil {
		t.Fatal(err)
	}
	routes := p.Routes()
	if len(routes) != 2 || routes[0].String() != "0.0.0.0/1" || routes[1].String() != "128.0.0.0/1" {
		t.Fatalf("wrong split defaults: %v", routes)
	}
	for _, destination := range []string{"1.1.1.1", "127.0.0.1", "128.0.0.1", "192.168.1.1", "255.255.255.254"} {
		hits := 0
		for _, route := range routes {
			if route.Contains(netip.MustParseAddr(destination)) {
				hits++
			}
		}
		if hits != 1 {
			t.Errorf("%s must be covered exactly once, got %d", destination, hits)
		}
	}
	uapi := p.UAPI(netip.MustParseAddr("198.51.100.1"))
	if !strings.Contains(uapi, "endpoint=198.51.100.1:51820\n") ||
		!strings.Contains(uapi, "header_protection_key="+strings.Repeat("01", 32)+"\n") {
		t.Fatal("management key mapping or endpoint pinning failed")
	}
	if strings.Contains(uapi, "allowed_ip=::/0") {
		t.Fatal("IPv4-only profiles must not forward IPv6")
	}
	metadata, _ := json.Marshal(p.Summary())
	if strings.Contains(string(metadata), "PrivateKey") || strings.Contains(string(metadata), strings.Repeat("01", 32)) {
		t.Fatal("metadata leaks keys")
	}
}

func TestDualStackRoutes(t *testing.T) {
	text := strings.Replace(config(), "Address = 10.110.0.2/32", "Address = 10.110.0.2/32, fd00::2/128", 1)
	p, err := Parse("Dual stack", text)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Routes()) != 4 || p.Routes()[3].String() != "8000::/1" {
		t.Fatal("missing IPv6 split defaults")
	}
	if !strings.Contains(p.UAPI(netip.MustParseAddr("2001:db8::1")), "endpoint=[2001:db8::1]:51820") {
		t.Fatal("IPv6 endpoint encoding")
	}
}

func TestSubnetEndpointDoesNotChangeFullTunnel(t *testing.T) {
	p, err := Parse("NAS", strings.Replace(config(), "[Peer]", "ListenPort = 51820\n[Peer]", 1))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := netip.MustParseAddr("192.0.2.1")
	uapi, err := p.SubnetUAPI(endpoint, netip.MustParsePrefix("10.110.0.0/16"))
	if err != nil || !strings.Contains(uapi, "allowed_ip=10.110.0.0/16\n") ||
		strings.Count(uapi, "allowed_ip=") != 1 || !strings.Contains(uapi, "listen_port=0\n") {
		t.Fatal("subnet endpoint settings", err)
	}
	if !strings.Contains(p.UAPI(endpoint), "allowed_ip=0.0.0.0/0\n") ||
		!strings.Contains(p.UAPI(endpoint), "listen_port=51820\n") || len(p.Routes()) != 2 {
		t.Fatal("subnet settings modified original profile")
	}
	for _, subnet := range []string{"0.0.0.0/0", "10.110.0.1/16", "10.111.0.0/16", "::/0", "10.110.0.2/32"} {
		if _, err := p.SubnetUAPI(endpoint, netip.MustParsePrefix(subnet)); err == nil {
			t.Fatal("accepted invalid subnet", subnet)
		}
	}
	if _, err := p.SubnetUAPI(netip.MustParseAddr("10.110.0.1"), netip.MustParsePrefix("10.110.0.0/16")); err == nil {
		t.Fatal("accepted endpoint loop")
	}
}

func TestUnsafeAndInvalidConfigsRejected(t *testing.T) {
	for name, text := range map[string]string{
		"scripts":                strings.Replace(config(), "[Peer]", "PostUp = powershell.exe\n[Peer]", 1),
		"unknown":                config() + "Run = arbitrary\n",
		"extra peer":             config() + "[Peer]\nPublicKey = secret\n",
		"duplicate":              strings.Replace(config(), "MTU = 1280", "MTU = 1280\nMTU = 1400", 1),
		"partial":                strings.Replace(config(), "0.0.0.0/0", "10.0.0.0/8", 1),
		"one half":               strings.Replace(config(), "0.0.0.0/0", "128.0.0.0/1", 1),
		"bad key":                strings.Replace(config(), "PrivateKey =", "PrivateKey = private-secret", 1),
		"nul":                    strings.Replace(config(), "51820", "51820\x00", 1),
		"bad dns":                strings.Replace(config(), "DNS = 1.1.1.1", "DNS = 127.0.0.1", 1),
		"v6 dns without address": strings.Replace(config(), "DNS = 1.1.1.1", "DNS = 2606:4700:4700::1111", 1),
		"mtu":                    strings.Replace(config(), "MTU = 1280", "MTU = 9000", 1),
		"junk size":              strings.Replace(config(), "Jmax = 80", "Jmax = 999999", 1),
		"negative packet length": strings.Replace(config(), "[Peer]", "I1 = <r -1>\n[Peer]", 1),
		"packet allocation":      strings.Replace(config(), "[Peer]", "I1 = <r 2147483647>\n[Peer]", 1),
		"combined packet length": strings.Replace(config(), "[Peer]", "I1 = <r 1000><rc 1000>\n[Peer]", 1),
		"header nonce space":     strings.Replace(config(), "S4 = 16", "S4 = 0", 1),
		"timer overflow":         strings.Replace(config(), "[Peer]", "RekeyTimeout = 4294967295\n[Peer]", 1),
		"missing dns":            strings.Replace(config(), "DNS = 1.1.1.1\n", "", 1),
		"oversize":               strings.Repeat("x", MaxSize+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("Test", text)
			if err == nil {
				t.Fatal("accepted invalid config")
			}

			if strings.Contains(err.Error(), "private-secret") {
				t.Fatal("private data in error")
			}
		})
	}
}

func TestBoundedPacketTemplate(t *testing.T) {
	p, err := Parse("Template", strings.Replace(config(), "[Peer]", "I1 = <b 0x1234><r 32><t><rc 4><rd 6>\n[Peer]", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.UAPI(netip.MustParseAddr("198.51.100.1")), "i1=<b 0x1234><r 32><t><rc 4><rd 6>") {
		t.Fatal("packet template not preserved")
	}
}

func guest(t *testing.T, config string) string {
	t.Helper()
	last, _ := json.Marshal(map[string]string{"config": config})
	value := map[string]any{"containers": []any{map[string]any{"container": "amnezia-awg2", "awg": map[string]string{"last_config": string(last)}}}}
	data, _ := json.Marshal(value)
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	w := zlib.NewWriter(&b)
	w.Write(data)
	w.Close()
	return "vpn://" + base64.RawURLEncoding.EncodeToString(b.Bytes())
}

func TestGuestMatchesManagementEnvelope(t *testing.T) {
	p, err := Parse("Guest", guest(t, config()))
	if err != nil {
		t.Fatal(err)
	}
	if p.Native != config() || p.Host != "node.example.com" {
		t.Fatal("guest did not unwrap native profile")
	}
}

func TestGuestBombAndTruncationRejected(t *testing.T) {
	for _, text := range []string{"vpn://AA", guest(t, strings.Repeat("a", MaxSize+1)), strings.TrimSuffix(guest(t, config()), "x") + "!"} {
		if _, err := Parse("Guest", text); err == nil {
			t.Fatal("accepted invalid guest")
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(config())
	f.Add("vpn://AAAA")
	f.Fuzz(func(t *testing.T, text string) {
		p, err := Parse("Fuzz", text)
		if err == nil {
			if len(p.Routes()) < 2 || len(p.Addresses) == 0 {
				t.Fatal("invalid successful parse")
			}
			p.UAPI(netip.MustParseAddr("198.51.100.1"))
		}
	})
}
