package gateway

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

func TestLinuxPrivateFilesAndLock(t *testing.T) {
	dir := privateTestDirectory(t)
	store := Store{Directory: dir}
	if err := store.SaveState(SavedState{Version: 1}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadState(); err == nil {
		t.Fatal("public state file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(SavedState{Version: 1}); err == nil {
		t.Fatal("symlink accepted")
	}
	first, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(dir); err == nil {
		second.Close()
		t.Fatal("concurrent lock acquired")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnixControlService(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only Unix credential test")
	}
	dir := privateTestDirectory(t)
	path := filepath.Join(dir, "control.sock")
	m, _, _, _, _ := managerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- ServeControl(ctx, path, m) }()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Error(err)
		}
	})
	eventually(t, 5*time.Second, func() bool { _, err := os.Stat(path); return err == nil })
	response, err := Call(ctx, path, Request{Action: "status"})
	if err != nil || response.Status.State != "disconnected" {
		t.Fatalf("%+v %v", response, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("control socket permissions", err)
	}
}

// This test creates a complete synthetic one-arm LAN inside private namespaces.
// The explicit container gates prevent accidental execution on a real gateway.
func TestLinuxGatewayNetworkNamespaces(t *testing.T) {
	if os.Getenv("OUTERNET_GATEWAY_NET_TEST") != "1" {
		t.Skip("opt-in disposable container test")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil || os.Geteuid() != 0 {
		t.Fatal("network tests require an explicitly disposable root Docker container")
	}
	if _, err := os.Stat(DataDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusing to touch existing gateway state; use a fresh disposable container")
	}
	t.Cleanup(func() {
		for _, path := range []string{
			filepath.Join(DataDirectory, "gateway.json"), filepath.Join(DataDirectory, "state.json"),
			filepath.Join(RuntimeDirectory, "control.sock"), filepath.Join(RuntimeDirectory, "owner.lock"),
		} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
		for _, path := range []string{DataDirectory, RuntimeDirectory} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
	})
	prefix := fmt.Sprintf("awg%d", os.Getpid())
	sw, gw, lan, wan := prefix+"s", prefix+"g", prefix+"l", prefix+"w"
	for _, ns := range []string{sw, gw, lan, wan} {
		testCommand(t, "ip", "netns", "add", ns)
		t.Cleanup(func() {
			if data, err := exec.Command("ip", "netns", "delete", ns).CombinedOutput(); err != nil {
				t.Errorf("remove test namespace: %s: %v", data, err)
			}
		})
		testCommand(t, "ip", "-n", ns, "link", "set", "lo", "up")
	}
	testCommand(t, "ip", "-n", sw, "link", "add", "br0", "type", "bridge")
	testCommand(t, "ip", "-n", sw, "link", "set", "br0", "up")
	for i, ns := range []string{gw, lan, wan} {
		port := fmt.Sprintf("port%d", i)
		testCommand(t, "ip", "-n", sw, "link", "add", port, "type", "veth", "peer", "name", "eth0", "netns", ns)
		testCommand(t, "ip", "-n", sw, "link", "set", port, "master", "br0")
		testCommand(t, "ip", "-n", sw, "link", "set", port, "up")
		testCommand(t, "ip", "-n", ns, "link", "set", "eth0", "up")
		testCommand(t, "ip", "netns", "exec", ns, "sysctl", "-q", "-w",
			"net.ipv6.conf.all.disable_ipv6=0", "net.ipv6.conf.default.disable_ipv6=0")
	}
	for ns, address := range map[string]string{gw: "192.0.2.2/24", lan: "192.0.2.3/24", wan: "192.0.2.1/24"} {
		testCommand(t, "ip", "-n", ns, "address", "add", address, "dev", "eth0")
	}
	for ns, address := range map[string]string{gw: "2001:db8:1::2/64", lan: "2001:db8:1::3/64", wan: "2001:db8:1::1/64"} {
		testCommand(t, "ip", "-n", ns, "-6", "address", "add", address, "dev", "eth0", "nodad")
	}
	testCommand(t, "ip", "-n", wan, "address", "add", "203.0.113.80/32", "dev", "lo")
	testCommand(t, "ip", "-n", wan, "address", "add", "9.9.9.9/32", "dev", "lo")
	testCommand(t, "ip", "-n", wan, "-6", "address", "add", "2001:db8:2::1/128", "dev", "lo", "nodad")
	testCommand(t, "ip", "-n", gw, "route", "add", "default", "via", "192.0.2.1")
	testCommand(t, "ip", "-n", lan, "route", "add", "default", "via", "192.0.2.2")
	testCommand(t, "ip", "-n", gw, "-6", "route", "add", "default", "via", "2001:db8:1::1")
	testCommand(t, "ip", "-n", lan, "-6", "route", "add", "default", "via", "2001:db8:1::2")
	testCommand(t, "ip", "netns", "exec", gw, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1", "net.ipv6.conf.all.forwarding=1")
	testCommand(t, "ip", "netns", "exec", gw, "nft", "add", "table", "inet", "unrelated_test")

	node := startTestProcess(t, namespaceHelper(wan, "node", ""))
	eventually(t, 10*time.Second, func() bool {
		_, err := namespaceHelper(wan, "http", "http://127.0.0.1:8080/ready").CombinedOutput()
		return err == nil
	})
	_ = node
	dir := t.TempDir()
	settings := filepath.Join(dir, "gateway.json")
	data, err := json.Marshal(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(settings, data, 0600); err != nil {
		t.Fatal(err)
	}
	configure := func(args ...string) string {
		t.Helper()
		return testCommand(t, "ip", append([]string{"netns", "exec", gw, "/usr/local/bin/outernetawg-gateway"}, args...)...)
	}
	configure("configure", "--file", settings)
	assertBlocked(t, lan)
	assertIPv6Blocked(t, lan)

	native := filepath.Join(dir, "test.conf")
	if err = os.WriteFile(native, []byte(networkProfile(t)), 0600); err != nil {
		t.Fatal(err)
	}
	startGateway := func() *testProcess {
		t.Helper()
		process := startTestProcess(t, exec.Command("ip", "netns", "exec", gw, "/usr/local/bin/outernetawg-gateway", "run"))
		eventually(t, 10*time.Second, func() bool {
			_, err := exec.Command("ip", "netns", "exec", gw, "/usr/local/bin/outernetawg-gateway", "status").CombinedOutput()
			return err == nil
		})
		return process
	}
	service := startGateway()
	configure("import", "--file", native, "--name", "Synthetic")
	configure("connect")
	waitConnected(t, gw)
	assertTunnel(t, lan)
	assertIPv6Blocked(t, lan)
	low, err := namespaceHelper(lan, "http", "http://9.9.9.9:8080/echo").CombinedOutput()
	if err != nil || strings.TrimSpace(string(low)) != "10.110.0.2" {
		t.Fatalf("lower-half IPv4 did not use AWG: %s %v", low, err)
	}
	if out, err := namespaceHelper(lan, "bulk", "http://203.0.113.80:8080/bulk").CombinedOutput(); err != nil {
		t.Fatalf("large bidirectional TCP transfer failed: %s %v", out, err)
	}
	dns, err := namespaceHelper(lan, "dns", "").CombinedOutput()
	if err != nil || strings.TrimSpace(string(dns)) != "dns:10.110.0.2" {
		t.Fatalf("DNS did not travel through the tunnel/SNAT: %s %v", dns, err)
	}
	route := testCommand(t, "ip", "-n", gw, "-j", "route", "get", "203.0.113.80")
	if !strings.Contains(route, `"gateway":"192.0.2.1"`) {
		t.Fatal("host default route changed:", route)
	}
	configure("disconnect")
	assertBlocked(t, lan)
	service.stop(t, false)
	next := testConfig()
	next.ProtectedSources = []string{"192.0.2.4/32"}
	nextData, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(settings, nextData, 0600); err != nil {
		t.Fatal(err)
	}
	configure("configure", "--file", settings)
	direct, err := namespaceHelper(lan, "http", "http://203.0.113.80:8080/echo").CombinedOutput()
	if err != nil || strings.TrimSpace(string(direct)) != "192.0.2.3" {
		t.Fatalf("removed source retained stale policy routes: %s %v", direct, err)
	}
	if err = os.WriteFile(settings, data, 0600); err != nil {
		t.Fatal(err)
	}
	configure("configure", "--file", settings)
	assertBlocked(t, lan)
	service = startGateway()
	configure("connect")
	waitConnected(t, gw)

	if out, err := namespaceHelper(wan, "http", "http://127.0.0.1:8080/deny").CombinedOutput(); err != nil {
		t.Fatalf("revoke synthetic peer: %s %v", out, err)
	}
	configure("disconnect")
	configure("connect")
	assertBlocked(t, lan)
	if out, err := namespaceHelper(wan, "http", "http://127.0.0.1:8080/allow").CombinedOutput(); err != nil {
		t.Fatalf("restore synthetic peer: %s %v", out, err)
	}
	waitConnected(t, gw)
	assertTunnel(t, lan)

	service.stop(t, true)
	assertBlocked(t, lan)
	testCommand(t, "ip", "netns", "exec", gw, "nft", "list", "table", "inet", filterTable)
	configure("protect")
	service = startGateway()
	waitConnected(t, gw)
	assertTunnel(t, lan)
	service.stop(t, false)
	assertBlocked(t, lan)

	if err = os.WriteFile(filepath.Join(DataDirectory, "gateway.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ip", "netns", "exec", gw, "/usr/local/bin/outernetawg-gateway", "protect").CombinedOutput(); err == nil || !strings.Contains(string(out), "ALL forwarding") {
		t.Fatalf("invalid settings did not fail closed: %s %v", out, err)
	}
	assertBlocked(t, lan)
	if err = os.WriteFile(filepath.Join(DataDirectory, "gateway.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	configure("unprotect", "--confirm-direct-routing")
	testCommand(t, "ip", "netns", "exec", gw, "nft", "list", "table", "inet", "unrelated_test")
	out, err := namespaceHelper(lan, "http", "http://203.0.113.80:8080/echo").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "192.0.2.3" {
		t.Fatalf("explicit unprotect did not restore direct routing: %s %v", out, err)
	}
}

func testCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %s: %v", name, args, out, err)
	}
	return string(out)
}

func eventually(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	until := time.Now().Add(timeout)
	for time.Now().Before(until) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for expected state")
}

func waitConnected(t *testing.T, ns string) {
	t.Helper()
	var last []byte
	eventually(t, 70*time.Second, func() bool {
		out, err := exec.Command("ip", "netns", "exec", ns, "/usr/local/bin/outernetawg-gateway", "status").CombinedOutput()
		last = out
		var status Status
		return err == nil && json.Unmarshal(out, &status) == nil && status.State == "connected" && status.Forwarding
	})
	t.Log("connected:", string(last))
}

func assertBlocked(t *testing.T, ns string) {
	t.Helper()
	if out, err := namespaceHelper(ns, "http", "http://203.0.113.80:8080/echo").CombinedOutput(); err == nil {
		t.Fatalf("protected IPv4 escaped the gateway: %s", out)
	}
}

func assertIPv6Blocked(t *testing.T, ns string) {
	t.Helper()
	if out, err := namespaceHelper(ns, "http", "http://[2001:db8:2::1]:8080/echo").CombinedOutput(); err == nil {
		t.Fatalf("IPv6 escaped the gateway: %s", out)
	}
}

func assertTunnel(t *testing.T, ns string) {
	t.Helper()
	out, err := namespaceHelper(ns, "http", "http://203.0.113.80:8080/echo").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "10.110.0.2" {
		t.Fatalf("expected AWG SNAT egress, got %s: %v", out, err)
	}
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *synchronizedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type testProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	output synchronizedBuffer
}

func startTestProcess(t *testing.T, cmd *exec.Cmd) *testProcess {
	t.Helper()
	p := &testProcess{cmd: cmd, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &p.output, &p.output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		p.stop(t, true)
		if t.Failed() {
			t.Log("helper output:", p.output.String())
		}
	})
	return p
}

func (p *testProcess) stop(t *testing.T, force bool) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	if force {
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
	} else if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Error(err)
	}
	select {
	case <-p.done:
	case <-time.After(55 * time.Second):
		t.Error("helper did not stop")
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		<-p.done
	}
}

func namespaceHelper(ns, role, url string) *exec.Cmd {
	cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestNamespaceHelper$")
	cmd.Env = append(os.Environ(), "OUTERNET_TEST_HELPER="+role, "OUTERNET_TEST_URL="+url)
	return cmd
}

func syntheticKeys(value byte) (private, public []byte) {
	private = bytes.Repeat([]byte{value}, 32)
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		panic(err)
	}
	return private, key.PublicKey().Bytes()
}

func networkProfile(t *testing.T) string {
	t.Helper()
	private, _ := syntheticKeys(3)
	_, public := syntheticKeys(7)
	native, _ := syntheticObfuscation()
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.110.0.2/32\nDNS = 203.0.113.80\nMTU = 1280\n%s[Peer]\nPublicKey = %s\nEndpoint = 192.0.2.1:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 1\n",
		base64.StdEncoding.EncodeToString(private), native, base64.StdEncoding.EncodeToString(public))
}

func syntheticObfuscation() (string, string) {
	var native, uapi strings.Builder
	for _, parameter := range [][2]string{
		{"Jc", "3"}, {"Jmin", "40"}, {"Jmax", "80"},
		{"S1", "16"}, {"S2", "16"}, {"S3", "16"}, {"S4", "16"},
		{"H1", "12345-12355"}, {"H2", "22345-22355"}, {"H3", "32345-32355"}, {"H4", "42345-42355"},
	} {
		fmt.Fprintf(&native, "%s = %s\n", parameter[0], parameter[1])
		fmt.Fprintf(&uapi, "%s=%s\n", strings.ToLower(parameter[0]), parameter[1])
	}
	header := bytes.Repeat([]byte{9}, 32)
	fmt.Fprintf(&native, "HeaderProtectionKey = %s\n", base64.StdEncoding.EncodeToString(header))
	fmt.Fprintf(&uapi, "header_protection_key=%s\n", hex.EncodeToString(header))
	return native.String(), uapi.String()
}

func TestNamespaceHelper(t *testing.T) {
	role := os.Getenv("OUTERNET_TEST_HELPER")
	if role == "" {
		return
	}
	if err := runHelper(role); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runHelper(role string) error {
	switch role {
	case "http":
		client := http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
		response, err := client.Get(os.Getenv("OUTERNET_TEST_URL"))
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, err = io.Copy(os.Stdout, io.LimitReader(response.Body, 4096))
		if response.StatusCode != http.StatusOK {
			return errors.New("helper HTTP request failed")
		}
		return err
	case "dns":
		conn, err := net.DialTimeout("udp4", "203.0.113.80:53", time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		if _, err = conn.Write([]byte("synthetic-dns-probe")); err != nil {
			return err
		}
		buffer := make([]byte, 256)
		n, err := conn.Read(buffer)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(buffer[:n])
		return err
	case "bulk":
		payload := bytes.Repeat([]byte("OuternetAWG-MTU-"), 16384)
		client := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		response, err := client.Post(os.Getenv("OUTERNET_TEST_URL"), "application/octet-stream", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		defer response.Body.Close()
		actual, err := io.ReadAll(io.LimitReader(response.Body, int64(len(payload)+1)))
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK || response.Header.Get("X-Test-Source") != "10.110.0.2" || !bytes.Equal(actual, payload) {
			return errors.New("bulk payload or tunneled source did not match")
		}
		return nil
	case "node":
		return helperNode()
	default:
		return errors.New("unknown test helper")
	}
}

func helperNode() error {
	t, err := tun.CreateTUN("srvawg0", 1280)
	if err != nil {
		return err
	}
	engine := device.NewDevice(t, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "synthetic node: "))
	defer engine.Close()
	private, _ := syntheticKeys(7)
	_, public := syntheticKeys(3)
	peer := hex.EncodeToString(public)
	_, obfuscation := syntheticObfuscation()
	config := "private_key=" + hex.EncodeToString(private) + "\nlisten_port=51820\n" + obfuscation + "replace_peers=true\npublic_key=" + peer + "\nallowed_ip=10.110.0.2/32\n"
	if err = engine.IpcSet(config); err != nil {
		return err
	}
	if err = engine.Up(); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"-4", "address", "add", "10.110.0.1/32", "dev", "srvawg0"},
		{"link", "set", "dev", "srvawg0", "up"},
		{"-4", "route", "add", "10.110.0.2/32", "dev", "srvawg0"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
	}
	dns, err := net.ListenPacket("udp4", "203.0.113.80:53")
	if err != nil {
		return err
	}
	defer dns.Close()
	go func() {
		buffer := make([]byte, 512)
		for {
			_, address, err := dns.ReadFrom(buffer)
			if err != nil {
				return
			}
			host, _, err := net.SplitHostPort(address.String())
			if err != nil {
				return
			}
			if _, err = dns.WriteTo([]byte("dns:"+host), address); err != nil {
				return
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.URL.Path {
		case "/deny":
			err = engine.IpcSet("public_key=" + peer + "\nremove=true\n")
		case "/allow":
			err = engine.IpcSet("public_key=" + peer + "\nreplace_allowed_ips=true\nallowed_ip=10.110.0.2/32\n")
		}
		if err != nil {
			http.Error(w, "synthetic node control failed", 500)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "invalid remote address", 500)
			return
		}
		if r.URL.Path == "/bulk" {
			body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024+1))
			if err != nil || len(body) > 512*1024 {
				http.Error(w, "invalid bulk payload", 400)
				return
			}
			w.Header().Set("X-Test-Source", host)
			if _, err := w.Write(body); err != nil {
				fmt.Fprintln(os.Stderr, "bulk response:", err)
			}
			return
		}
		fmt.Fprint(w, host)
	})
	server := http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	return server.ListenAndServe()
}
