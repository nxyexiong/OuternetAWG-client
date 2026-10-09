package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

func testConfig() Config {
	return Config{IngressInterface: "eth0", TunnelInterface: "oawg0", ProtectedSources: []string{"192.0.2.3/32"}}
}

func privateTestDirectory(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testProfile() string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.110.0.2/32\nDNS = 1.1.1.1\nMTU = 1280\n" +
		"[Peer]\nPublicKey = " + key + "\nEndpoint = 198.51.100.1:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n"
}

func parsedProfile(t *testing.T) *profile.Profile {
	t.Helper()
	p, err := profile.Parse("Gateway", testProfile())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigValidation(t *testing.T) {
	valid := `{"ingress_interface":"eth0","protected_sources":["192.0.2.3/32"]}`
	c, err := DecodeConfig([]byte(valid))
	if err != nil || c.TunnelInterface != "oawg0" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for name, text := range map[string]string{
		"unknown":       strings.Replace(valid, `"eth0"`, `"eth0","script":"bad"`, 1),
		"trailing":      valid + "{}",
		"empty":         `{}`,
		"oversized":     strings.Repeat(" ", MaxConfigSize+1),
		"shell":         strings.Replace(valid, "eth0", "eth0;sh", 1),
		"loopback":      strings.Replace(valid, "eth0", "lo", 1),
		"uncanonical":   strings.Replace(valid, "192.0.2.3/32", "192.0.2.3/24", 1),
		"overlap":       strings.Replace(valid, `"192.0.2.3/32"`, `"192.0.2.3/32","192.0.2.0/24"`, 1),
		"IPv6":          strings.Replace(valid, "192.0.2.3/32", "2001:db8::/64", 1),
		"default route": strings.Replace(valid, "192.0.2.3/32", "0.0.0.0/0", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfig([]byte(text)); err == nil {
				t.Fatal("accepted invalid settings")
			}
		})
	}
	c.TunnelInterface = c.IngressInterface
	if c.Validate() == nil {
		t.Fatal("same interface accepted")
	}
}

func TestGatewayProfileConstraints(t *testing.T) {
	c := testConfig()
	if err := c.ValidateProfile(parsedProfile(t)); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string][2]string{
		"IPv6":           {"10.110.0.2/32", "10.110.0.2/32, fd00::2/128"},
		"subnet":         {"10.110.0.2/32", "10.110.0.2/24"},
		"overlap":        {"10.110.0.2/32", "192.0.2.3/32"},
		"keepalive":      {"PersistentKeepalive = 25", "PersistentKeepalive = 0"},
		"slow keepalive": {"PersistentKeepalive = 25", "PersistentKeepalive = 121"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := profile.Parse("Test", strings.Replace(testProfile(), change[0], change[1], 1))
			if err != nil {
				t.Fatal(err)
			}
			if c.ValidateProfile(p) == nil {
				t.Fatal("accepted unsupported gateway profile")
			}
		})
	}
}

type memoryStore struct {
	state SavedState
	err   error
}

func (s *memoryStore) LoadState() (SavedState, error) { return s.state, s.err }
func (s *memoryStore) SaveState(value SavedState) error {
	if s.err != nil {
		return s.err
	}
	s.state = value
	return nil
}

type fakeNetwork struct {
	events *[]string
	fail   string
}

func (n *fakeNetwork) event(value string) error {
	*n.events = append(*n.events, value)
	if n.fail == value {
		return errors.New("injected " + value + " failure")
	}
	return nil
}
func (n *fakeNetwork) Protect(context.Context) error                     { return n.event("protect") }
func (n *fakeNetwork) Check(context.Context, *profile.Profile) error     { return n.event("check") }
func (n *fakeNetwork) Configure(context.Context, *profile.Profile) error { return n.event("configure") }
func (n *fakeNetwork) Permit(context.Context) error                      { return n.event("permit") }
func (n *fakeNetwork) Deny(context.Context) error                        { return n.event("deny") }

type fakeSession struct {
	events  *[]string
	traffic Traffic
	err     error
}

func (s *fakeSession) Stats() (Traffic, error) { return s.traffic, s.err }
func (s *fakeSession) Close() error {
	*s.events = append(*s.events, "close")
	return nil
}

func managerFixture(t *testing.T) (*Manager, *memoryStore, *fakeNetwork, *fakeSession, *[]string) {
	t.Helper()
	events := []string{}
	store := &memoryStore{state: SavedState{Version: 1, Name: "Test", Native: testProfile()}}
	network := &fakeNetwork{events: &events}
	session := &fakeSession{events: &events}
	connect := func(context.Context, Config, *profile.Profile) (Session, error) {
		events = append(events, "engine")
		return session, nil
	}
	m, err := NewManager(context.Background(), testConfig(), store, network, connect)
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	return m, store, network, session, &events
}

func TestLifecycleRequiresHandshakeAndPersistsPause(t *testing.T) {
	m, store, _, session, events := managerFixture(t)
	ctx, now := context.Background(), time.Unix(1700000000, 0)
	if err := m.Connect(ctx, now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"protect", "check", "engine", "configure"}) || !store.state.Desired {
		t.Fatalf("wrong start ordering: %v", *events)
	}
	m.Tick(ctx, now.Add(time.Second))
	if m.Status().Forwarding {
		t.Fatal("forwarding before handshake")
	}
	session.traffic = Traffic{Handshake: now, Upload: 12, Download: 34}
	m.Tick(ctx, now.Add(2*time.Second))
	if s := m.Status(); s.State != "connected" || !s.Forwarding || s.Traffic.Upload != 12 || s.Traffic.Download != 34 {
		t.Fatalf("wrong status: %+v", s)
	}
	if err := m.Import("New", testProfile()); err == nil {
		t.Fatal("replaced active profile")
	}
	*events = nil
	if err := m.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"deny", "close"}) || store.state.Desired {
		t.Fatalf("wrong stop ordering or pause: %v", *events)
	}
	m.Tick(ctx, now.Add(time.Hour))
	if m.Status().Forwarding || len(*events) != 2 {
		t.Fatal("a paused gateway reconnected")
	}
}

func TestEveryConnectionFailureRemainsProtected(t *testing.T) {
	for _, stage := range []string{"protect", "check", "configure", "permit"} {
		t.Run(stage, func(t *testing.T) {
			m, store, network, session, events := managerFixture(t)
			now := time.Unix(1700000000, 0)
			network.fail = stage
			err := m.Connect(context.Background(), now)
			if stage == "permit" {
				session.traffic.Handshake = now
				m.Tick(context.Background(), now.Add(time.Second))
			} else if err == nil {
				t.Fatal("missing setup error")
			}
			s := m.Status()
			if s.Forwarding || s.Error == "" || s.State != "reconnecting" || s.RetryAt == nil || !store.state.Desired {
				t.Fatalf("failure was hidden: %+v", s)
			}
			joined := strings.Join(*events, ",")
			if !strings.Contains(joined, "deny") || strings.Contains(joined, "close,deny") {
				t.Fatal("unsafe failure teardown:", joined)
			}
			network.fail = ""
			m.Tick(context.Background(), now.Add(2*time.Minute))
			if m.Status().State != "connecting" {
				t.Fatal("did not retry")
			}
		})
	}
}

func TestStaleHandshakeClosesBeforeRetry(t *testing.T) {
	m, _, _, session, events := managerFixture(t)
	now := time.Unix(1700000000, 0)
	if err := m.Connect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	session.traffic.Handshake = now
	m.Tick(context.Background(), now.Add(time.Second))
	*events = nil
	m.Tick(context.Background(), now.Add(3*time.Minute))
	if m.Status().Forwarding || m.Status().State != "reconnecting" || (*events)[0] != "deny" || (*events)[len(*events)-1] != "close" {
		t.Fatalf("stale handshake admitted traffic: %+v %v", m.Status(), *events)
	}
}

func TestPausePersistenceFailureStillDisconnectsAndReports(t *testing.T) {
	m, store, _, _, events := managerFixture(t)
	if err := m.Connect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("disk full")
	*events = nil
	err := m.Disconnect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "later service start may reconnect") ||
		!reflect.DeepEqual(*events, []string{"deny", "close"}) || m.Status().Desired {
		t.Fatalf("failed pause was hidden: %v %+v %v", err, m.Status(), *events)
	}
}

func TestServiceStopPreservesAutoconnect(t *testing.T) {
	m, store, _, _, events := managerFixture(t)
	if err := m.Connect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	*events = nil
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.state.Desired || !reflect.DeepEqual(*events, []string{"deny", "close"}) {
		t.Fatal("service stop changed desired state")
	}
	if m.Connect(context.Background(), time.Now()) == nil {
		t.Fatal("closed manager accepted connect")
	}
}

func TestSavedConfigAndStateAreAtomicAndBounded(t *testing.T) {
	store := Store{Directory: privateTestDirectory(t)}
	if err := store.SaveConfig(testConfig()); err != nil {
		t.Fatal(err)
	}
	c, err := store.LoadConfig()
	if err != nil || !reflect.DeepEqual(c, testConfig()) {
		t.Fatalf("%+v %v", c, err)
	}
	value := SavedState{Version: 1, Name: "Test", Native: testProfile(), Desired: true}
	if err := store.SaveState(value); err != nil {
		t.Fatal(err)
	}
	value.Desired = false
	if err := store.SaveState(value); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadState()
	if err != nil || got != value {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, text := range []string{`{"version":2}`, `{"version":1,"desired":true}`, strings.Repeat("x", MaxMessageSize+1)} {
		if err := os.WriteFile(filepath.Join(store.Directory, "state.json"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadState(); err == nil {
			t.Fatal("accepted broken saved state")
		}
	}
	pending, err := filepath.Glob(filepath.Join(store.Directory, ".pending-*"))
	if err != nil || len(pending) != 0 {
		t.Fatal("private temporary files leaked", pending, err)
	}
}

func TestControlBoundsAndSecretRedaction(t *testing.T) {
	var b bytes.Buffer
	want := Request{Version: 1, Action: "import", Name: "Test", Content: testProfile()}
	if err := writeFrame(&b, want); err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := readFrame(&b, &got); err != nil || got != want {
		t.Fatal("framing", err)
	}
	for _, size := range []uint32{0, MaxMessageSize + 1} {
		b.Reset()
		if err := binary.Write(&b, binary.LittleEndian, size); err != nil {
			t.Fatal(err)
		}
		if readFrame(&b, &got) == nil {
			t.Fatal("invalid size accepted")
		}
	}
	b.Reset()
	private := []byte(`{"secret-private-key":"bad"}`)
	if err := binary.Write(&b, binary.LittleEndian, uint32(len(private))); err != nil {
		t.Fatal(err)
	}
	b.Write(private)
	if err := readFrame(&b, &got); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unredacted parse error", err)
	}
	m, _, _, _, _ := managerFixture(t)
	for _, req := range []Request{{Version: 2, Action: "status"}, {Version: 1, Action: "exec"}, {Version: 1, Action: "status", Content: "private"}} {
		if response := dispatch(context.Background(), m, req); response.Error == "" {
			t.Fatal("invalid command accepted")
		}
	}
}

type recordingRunner struct {
	commands []string
	scripts  []string
	tables   string
	rules    string
	routes   string
	address  string
	fail     string
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string, input string) ([]byte, error) {
	command := name + " " + strings.Join(args, " ")
	r.commands = append(r.commands, command)
	if command == r.fail {
		return nil, errors.New("injected command failure")
	}
	if input != "" {
		r.scripts = append(r.scripts, input)
		return nil, nil
	}
	var text string
	switch command {
	case "nft -j list ruleset":
		text = r.tables
		if text == "" {
			text = `{"nftables":[]}`
		}
	case "ip -j -4 rule show":
		text = r.rules
	case "ip -j -4 route show table all":
		text = r.routes
	case "ip -j address show":
		text = r.address
		if text == "" {
			text = `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.2.2","prefixlen":24}]}]`
		}
	}
	if text == "" {
		text = "[]"
	}
	return []byte(text), nil
}

func TestFirewallScopeAndFailClosedRouting(t *testing.T) {
	r := &recordingRunner{}
	n := LinuxNetwork{Config: testConfig(), Runner: r}
	ctx := context.Background()
	if err := n.Protect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n.Check(ctx, parsedProfile(t)); err != nil {
		t.Fatal(err)
	}
	if err := n.Configure(ctx, parsedProfile(t)); err != nil {
		t.Fatal(err)
	}
	if err := n.Permit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n.Deny(ctx); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.commands, "\n") + "\n" + strings.Join(r.scripts, "\n")
	for _, expected := range []string{
		`iifname "eth0" ip saddr @sources0 drop`,
		`iifname "eth0" meta nfproto ipv6 drop`,
		`oifname "eth0" icmp type redirect drop`,
		`iifname "oawg0" drop`,
		`snat to 10.110.0.2`,
		`maxseg size set 1240`,
		`unreachable default table 21110 proto 196`,
		`from 192.0.2.3/32 iif eth0 protocol 196 prohibit`,
		`from 192.0.2.3/32 iif eth0 protocol 196 lookup 21110`,
		`timeout 15s`,
	} {
		if !strings.Contains(all, expected) {
			t.Error("missing", expected)
		}
	}
	for _, forbidden := range []string{"flush ruleset", "table main", "0.0.0.0/1", "iptables", "ip route del default"} {
		if strings.Contains(all, forbidden) {
			t.Error("unsafe networking operation", forbidden)
		}
	}
	if strings.Contains(all, "%!") {
		t.Fatal("malformed formatted script", all)
	}
}

func TestForeignNetworkingAndUnsupportedTopologyRejected(t *testing.T) {
	for name, runner := range map[string]*recordingRunner{
		"table":           {tables: `{"nftables":[{"table":{"family":"inet","name":"outernetawg_gateway"}}]}`},
		"flow":            {tables: `{"nftables":[{"flowtable":{"name":"fast"}}]}`},
		"rules":           {rules: `[{"priority":11000,"protocol":"static"}]`},
		"routes":          {routes: `[{"table":21110,"protocol":"static"}]`},
		"existing tunnel": {address: `[{"ifname":"oawg0"}]`},
		"off-link":        {address: `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.3.2","prefixlen":24}]}]`},
		"overlap":         {address: `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"10.110.0.1","prefixlen":24}]}]`},
	} {
		t.Run(name, func(t *testing.T) {
			n := LinuxNetwork{Config: testConfig(), Runner: runner}
			err := n.Protect(context.Background())
			if err == nil {
				err = n.Check(context.Background(), parsedProfile(t))
			}
			if err == nil {
				t.Fatal("accepted conflicting network state")
			}
		})
	}
}

func TestGuardReconfigurationCoversOldAndNew(t *testing.T) {
	c, next := testConfig(), testConfig()
	next.IngressInterface = "eth1"
	next.ProtectedSources = []string{"192.0.3.4/32"}
	script := guardScript(next, c)
	for _, text := range []string{`iifname "eth0" ip saddr @sources1 drop`, `iifname "eth1" ip saddr @sources0 drop`} {
		if !strings.Contains(script, text) {
			t.Fatal("reconfiguration protection gap", script)
		}
	}
	if strings.Contains(script, "%!") {
		t.Fatal("malformed guard", script)
	}
}

func TestLegacyNftJSONOwnershipMarker(t *testing.T) {
	r := &recordingRunner{tables: `{"nftables":[{"table":{"family":"inet","name":"outernetawg_gateway"}},{"rule":{"family":"inet","table":"outernetawg_gateway","chain":"ownership","comment":"OuternetAWG gateway v1"}}]}`}
	n := LinuxNetwork{Config: testConfig(), Runner: r}
	if err := n.Protect(context.Background()); err != nil {
		t.Fatal("nft 1.0.6 ownership metadata rejected:", err)
	}
	r.tables = strings.Replace(r.tables, `"ownership"`, `"unrelated"`, 1)
	if err := n.Protect(context.Background()); err == nil {
		t.Fatal("unrelated chain comment claimed ownership")
	}
}

func TestCommandOutputLimitAppliesToIOCopy(t *testing.T) {
	var buffer limitedBuffer
	_, err := io.Copy(&buffer, strings.NewReader(strings.Repeat("a", 4*1024*1024+1)))
	if err == nil || buffer.Len() > 4*1024*1024 {
		t.Fatal("command output limit bypassed via io.Copy")
	}
}

func TestUnprotectNeverDeletesForeignRules(t *testing.T) {
	r := &recordingRunner{rules: `[{"priority":42,"protocol":196},{"priority":11000,"protocol":196},{"priority":11064,"protocol":196}]`,
		routes: `[{"table":21110,"protocol":196}]`,
		tables: `{"nftables":[{"table":{"family":"inet","name":"outernetawg_gateway","comment":"OuternetAWG gateway v1"}},{"table":{"family":"inet","name":"other"}}]}`}
	n := LinuxNetwork{Config: testConfig(), Runner: r}
	if err := n.Unprotect(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.commands, "\n") + strings.Join(r.scripts, "\n")
	if strings.Contains(all, "del pref 42") || strings.Contains(all, "delete table inet other") || strings.Contains(all, "flush ruleset") {
		t.Fatal("unprotect touched foreign state", all)
	}
}

func TestProfileSummaryContainsNoPrivateMaterial(t *testing.T) {
	m, _, _, _, _ := managerFixture(t)
	data, err := json.Marshal(m.Status())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PrivateKey") || strings.Contains(string(data), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))) {
		t.Fatal("status leaks private profile")
	}
	if !strings.Contains(string(data), netip.MustParseAddr("1.1.1.1").String()) {
		t.Fatal("DNS missing from status")
	}
}

func FuzzControl(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		var request Request
		err := readFrame(bytes.NewReader(data), &request)
		if err != nil && !errors.Is(err, io.EOF) && strings.Contains(err.Error(), "PrivateKey") {
			t.Fatal("private key in protocol error")
		}
	})
}
