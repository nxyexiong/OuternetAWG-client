package nas

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

func TestPrivateFilesAndStatus(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned file validation")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := PrepareRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if other, err := PrepareRuntime(dir); err == nil {
		other.Close()
		t.Fatal("duplicate daemon acquired lock")
	}
	c := testConfig(t)
	c.ProfileFile = filepath.Join(dir, "profile")
	if err := os.WriteFile(c.ProfileFile, []byte(testProfile(t).Native), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(c); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.ProfileFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(c); err == nil {
		t.Fatal("world-readable profile accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(c.ProfileFile, link); err != nil {
		t.Fatal(err)
	}
	if file, err := privateFile(link); err == nil {
		file.Close()
		t.Fatal("symlink accepted")
	}
	s := Status{State: "waiting", UpdatedAt: time.Now().UTC()}
	if err := WriteStatus(dir, s); err != nil {
		t.Fatal(err)
	}
	actual, err := ReadStatus(dir)
	if err != nil || actual.State != s.State || !actual.UpdatedAt.Equal(s.UpdatedAt) {
		t.Fatal("status round trip", actual, err)
	}
	info, err := os.Stat(filepath.Join(dir, "status.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("status permissions", err)
	}
}

func TestNASNetworkNamespaces(t *testing.T) {
	if os.Getenv("OUTERNET_NAS_NET_TEST") != "1" {
		t.Skip("opt-in disposable container networking test")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil || os.Geteuid() != 0 {
		t.Fatal("requires a disposable root Docker container")
	}
	if _, err := os.Stat(RuntimeDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusing existing NAS runtime state")
	}
	t.Cleanup(func() {
		for _, path := range []string{filepath.Join(RuntimeDirectory, "status.json"), filepath.Join(RuntimeDirectory, "owner.lock"), RuntimeDirectory} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
	})
	prefix := fmt.Sprintf("nas%d", os.Getpid())
	sw, host, node, peer := prefix+"s", prefix+"h", prefix+"n", prefix+"p"
	for _, ns := range []string{sw, host, node, peer} {
		command(t, "ip", "netns", "add", ns)
		t.Cleanup(func() { command(t, "ip", "netns", "delete", ns) })
		command(t, "ip", "-n", ns, "link", "set", "lo", "up")
	}
	command(t, "ip", "-n", sw, "link", "add", "br0", "type", "bridge")
	command(t, "ip", "-n", sw, "link", "set", "br0", "up")
	for i, ns := range []string{node, host, peer} {
		port := fmt.Sprintf("p%d", i)
		command(t, "ip", "-n", sw, "link", "add", port, "type", "veth", "peer", "name", "eth0", "netns", ns)
		command(t, "ip", "-n", sw, "link", "set", port, "master", "br0")
		command(t, "ip", "-n", sw, "link", "set", port, "up")
		command(t, "ip", "-n", ns, "link", "set", "eth0", "up")
		command(t, "ip", "-n", ns, "address", "add", fmt.Sprintf("192.0.2.%d/24", i+1), "dev", "eth0")
	}
	command(t, "ip", "-n", host, "address", "add", "192.0.2.22/32", "dev", "eth0")
	command(t, "ip", "-n", host, "route", "add", "default", "via", "192.0.2.1")
	command(t, "ip", "netns", "exec", host, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1")
	command(t, "ip", "netns", "exec", node, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1")
	command(t, "ip", "netns", "exec", host, "nft", "add", "table", "inet", "unrelated_nas_test")
	baseline := func() string {
		return command(t, "ip", "-n", host, "-j", "route", "show", "default") +
			command(t, "ip", "-n", host, "-j", "-6", "route", "show") +
			command(t, "ip", "-n", host, "-j", "rule", "show") +
			command(t, "ip", "netns", "exec", host, "sysctl", "-n", "net.ipv4.ip_forward", "net.ipv6.conf.all.forwarding", "net.ipv4.conf.all.rp_filter") +
			command(t, "ip", "netns", "exec", host, "nft", "-j", "list", "ruleset") +
			command(t, "ip", "netns", "exec", host, "cat", "/etc/resolv.conf")
	}
	before := baseline()
	startProcess(t, helper(node, "node", ""))
	startProcess(t, helper(host, "services", ""))
	startProcess(t, helper(peer, "peer", ""))
	waitFor(t, 10*time.Second, func() bool { return requestOK(node, "http://127.0.0.1:18080/ready") })
	waitFor(t, 10*time.Second, func() bool { return requestOK(host, "http://127.0.0.1:18080/ready") })

	file := filepath.Join(t.TempDir(), "nas.conf")
	if err := os.WriteFile(file, []byte(syntheticProfile()), 0600); err != nil {
		t.Fatal(err)
	}
	startNAS := func() *process {
		cmd := exec.Command("ip", "netns", "exec", host, "setpriv", "--bounding-set=-all,+net_admin", "--inh-caps=-all",
			"--ambient-caps=-all", "--no-new-privs", "/usr/local/bin/outernetawg-nas", "run")
		cmd.Env = append(os.Environ(), "OUTERNETAWG_PROFILE_FILE="+file)
		return startProcess(t, cmd)
	}
	status := func() Status {
		out, err := exec.Command("ip", "netns", "exec", host, "outernetawg-nas", "status").Output()
		var s Status
		if err == nil {
			if err := json.Unmarshal(out, &s); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	waitConnected := func() {
		waitFor(t, 80*time.Second, func() bool { return status().State == "connected" })
	}
	client := startNAS()
	waitConnected()
	command(t, "ip", "netns", "exec", host, "outernetawg-nas", "healthcheck")
	if after := baseline(); before != after {
		t.Fatalf("NAS host networking/DNS/firewall changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
	route := command(t, "ip", "-n", host, "-j", "route", "get", "10.110.0.3")
	if !strings.Contains(route, `"dev":"oawgnas0"`) || !strings.Contains(route, `"prefsrc":"10.110.0.2"`) {
		t.Fatal("subnet route/source mismatch", route)
	}
	command(t, "ip", "netns", "exec", host, os.Args[0], "-test.run=^TestExclusiveTUNHelper$")
	assertServices := func() {
		t.Helper()
		waitFor(t, 10*time.Second, func() bool { return requestOK(peer, "http://10.110.0.2:18080/echo") })
		if out, err := helper(peer, "bulk", "http://10.110.0.2:18080/bulk").CombinedOutput(); err != nil {
			t.Fatalf("large TCP transfer: %s: %v", out, err)
		}
		if out, err := helper(peer, "udp", "10.110.0.2:18181").CombinedOutput(); err != nil {
			t.Fatalf("UDP transfer: %s: %v", out, err)
		}
	}
	assertServices()
	if requestOK(peer, "http://192.0.2.22:18080/echo") {
		t.Fatal("tunnel exposed NAS LAN address")
	}
	waitFor(t, 5*time.Second, func() bool { return status().Dropped > 0 })

	// A real node-side peer revocation must make health stale and trigger retry.
	if !requestOK(node, "http://127.0.0.1:18080/deny") {
		t.Fatal("could not revoke synthetic NAS peer")
	}
	if requestOK(peer, "http://10.110.0.2:18080/echo") {
		t.Fatal("revoked NAS peer remained reachable")
	}
	waitFor(t, 195*time.Second, func() bool { return status().State == "reconnecting" })
	if err := exec.Command("ip", "netns", "exec", host, "outernetawg-nas", "healthcheck").Run(); err == nil {
		t.Fatal("revoked session passed healthcheck")
	}
	if !requestOK(node, "http://127.0.0.1:18080/allow") {
		t.Fatal("could not restore synthetic NAS peer")
	}
	waitConnected()
	assertServices()

	client.stop(t, false)
	waitFor(t, 10*time.Second, func() bool {
		return !strings.Contains(command(t, "ip", "-n", host, "-j", "link", "show"), `"ifname":"oawgnas0"`)
	})
	if before != baseline() || strings.Contains(command(t, "ip", "-n", host, "-4", "route", "show", "table", "all"), "10.110.") {
		t.Fatal("stop did not restore original host networking")
	}
	client = startNAS()
	waitConnected()
	client.stop(t, true)
	waitFor(t, 10*time.Second, func() bool {
		return !strings.Contains(command(t, "ip", "-n", host, "-j", "link", "show"), `"ifname":"oawgnas0"`)
	})
	client = startNAS()
	waitConnected()
	assertServices()
	client.stop(t, false)
	if before != baseline() {
		t.Fatal("restart changed host network baseline")
	}
}

func TestExclusiveTUNHelper(t *testing.T) {
	if os.Getenv("OUTERNET_NAS_NET_TEST") != "1" {
		t.Skip("opt-in helper")
	}
	// Only the namespace helper has an existing NAS TUN.
	if _, err := net.InterfaceByName("oawgnas0"); err != nil {
		t.Skip("no test NAS interface")
	}
	if device, err := exclusiveTUN("oawgnas0", 1280); err == nil {
		device.Close()
		t.Fatal("attached to an existing TUN")
	}
}

func command(t *testing.T, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %s: %v", name, args, out, err)
	}
	return string(out)
}

func waitFor(t *testing.T, timeout time.Duration, test func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if test() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("condition timed out after", timeout)
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

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type process struct {
	cmd    *exec.Cmd
	done   chan struct{}
	output synchronizedBuffer
}

func startProcess(t *testing.T, cmd *exec.Cmd) *process {
	t.Helper()
	p := &process{cmd: cmd, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &p.output, &p.output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		p.stop(t, true)
		if t.Failed() || strings.Contains(p.output.String(), "DATA RACE") {
			t.Log("process output:", p.output.String())
		}
		if strings.Contains(p.output.String(), "DATA RACE") {
			t.Error("race in helper/daemon")
		}
	})
	return p
}

func (p *process) stop(t *testing.T, force bool) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	var err error
	if force {
		err = p.cmd.Process.Kill()
	} else {
		err = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Error(err)
	}
	select {
	case <-p.done:
	case <-time.After(65 * time.Second):
		t.Error("test process did not stop")
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		<-p.done
	}
}

func helper(ns, role, target string) *exec.Cmd {
	cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestNASHelper$")
	cmd.Env = append(os.Environ(), "NAS_TEST_ROLE="+role, "NAS_TEST_TARGET="+target)
	return cmd
}

func requestOK(ns, url string) bool {
	return helper(ns, "http", url).Run() == nil
}

func keys(value byte) (string, string) {
	key, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{value}, 32))
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(key.Bytes()), hex.EncodeToString(key.PublicKey().Bytes())
}

func obfuscation() (string, string) {
	var native, uapi strings.Builder
	for _, item := range [][2]string{
		{"Jc", "3"}, {"Jmin", "40"}, {"Jmax", "80"},
		{"S1", "16"}, {"S2", "16"}, {"S3", "16"}, {"S4", "16"},
		{"H1", "12345-12355"}, {"H2", "22345-22355"}, {"H3", "32345-32355"}, {"H4", "42345-42355"},
	} {
		fmt.Fprintf(&native, "%s = %s\n", item[0], item[1])
		fmt.Fprintf(&uapi, "%s=%s\n", strings.ToLower(item[0]), item[1])
	}
	key := bytes.Repeat([]byte{9}, 32)
	fmt.Fprintf(&native, "HeaderProtectionKey = %s\n", base64.StdEncoding.EncodeToString(key))
	fmt.Fprintf(&uapi, "header_protection_key=%s\n", hex.EncodeToString(key))
	return native.String(), uapi.String()
}

func syntheticProfile() string {
	_, public := keys(7)
	publicBytes, _ := hex.DecodeString(public)
	native, _ := obfuscation()
	return "[Interface]\nPrivateKey = " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)) +
		"\nAddress = 10.110.0.2/32\nDNS = 1.1.1.1\nMTU = 1280\n" + native +
		"[Peer]\nPublicKey = " + base64.StdEncoding.EncodeToString(publicBytes) +
		"\nEndpoint = 192.0.2.1:51820\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 1\n"
}

func TestNASHelper(t *testing.T) {
	role := os.Getenv("NAS_TEST_ROLE")
	if role == "" {
		return
	}
	if err := runHelper(role, os.Getenv("NAS_TEST_TARGET")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runHelper(role, target string) error {
	switch role {
	case "http", "bulk":
		payload := bytes.Repeat([]byte("OuternetAWG-NAS-"), 32768)
		client := http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		var response *http.Response
		var err error
		if role == "bulk" {
			response, err = client.Post(target, "application/octet-stream", bytes.NewReader(payload))
		} else {
			response, err = client.Get(target)
		}
		if err != nil {
			return err
		}
		defer response.Body.Close()
		actual, err := io.ReadAll(io.LimitReader(response.Body, int64(len(payload)+1)))
		if err != nil {
			return err
		}
		if response.StatusCode != 200 {
			return errors.New("HTTP service failed")
		}
		if role == "bulk" && (!bytes.Equal(actual, payload) || response.Header.Get("X-Source") != "10.110.0.3") {
			return errors.New("TCP transfer bytes or VPN source mismatch")
		}
		return nil
	case "udp":
		connection, err := net.DialTimeout("udp4", target, time.Second)
		if err != nil {
			return err
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			return err
		}
		data := bytes.Repeat([]byte{42}, 1000)
		if _, err := connection.Write(data); err != nil {
			return err
		}
		buffer := make([]byte, 1500)
		n, err := connection.Read(buffer)
		if err != nil {
			return err
		}
		if !bytes.Equal(buffer[:n], data) {
			return errors.New("UDP payload mismatch")
		}
		return nil
	case "node", "peer":
		return runSyntheticEngine(role)
	case "services":
		return serveNAS(nil)
	default:
		return errors.New("unknown test helper")
	}
}

func runSyntheticEngine(role string) error {
	private, _ := keys(7)
	_, nasPublic := keys(3)
	_, peerPublic := keys(5)
	name, address, port := "srvawg0", "10.110.0.1/32", "51820"
	if role == "peer" {
		private, _ = keys(5)
		name, address, port = "peerawg0", "10.110.0.3/32", "0"
	}
	t, err := tun.CreateTUN(name, 1280)
	if err != nil {
		return err
	}
	engine := device.NewDevice(t, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "synthetic: "))
	defer engine.Close()
	_, awg := obfuscation()
	config := "private_key=" + private + "\nlisten_port=" + port + "\n" + awg + "replace_peers=true\n"
	nasPeer := "public_key=" + nasPublic + "\nreplace_allowed_ips=true\nallowed_ip=10.110.0.2/32\nallowed_ip=192.0.2.22/32\n"
	if role == "node" {
		config += nasPeer + "public_key=" + peerPublic + "\nallowed_ip=10.110.0.3/32\n"
	} else {
		_, serverPublic := keys(7)
		config += "public_key=" + serverPublic + "\nendpoint=192.0.2.1:51820\npersistent_keepalive_interval=1\nallowed_ip=0.0.0.0/0\n"
	}
	if err := engine.IpcSet(config); err != nil {
		return err
	}
	if err := engine.Up(); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"address", "add", address, "dev", name},
		{"link", "set", "dev", name, "up"},
		{"route", "add", "10.110.0.0/16", "dev", name},
		{"route", "add", "192.0.2.22/32", "dev", name},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
	}
	if role == "node" {
		return serveNAS(func(action string) error {
			if action == "/deny" {
				return engine.IpcSet("public_key=" + nasPublic + "\nremove=true\n")
			}
			if action == "/allow" {
				return engine.IpcSet(nasPeer)
			}
			return nil
		})
	}
	<-engine.Wait()
	return nil
}

func serveNAS(control func(string) error) error {
	udp, err := net.ListenPacket("udp4", ":18181")
	if err != nil {
		return err
	}
	defer udp.Close()
	if control == nil {
		go func() {
			buffer := make([]byte, 1500)
			for {
				n, peer, err := udp.ReadFrom(buffer)
				if err != nil {
					return
				}
				if _, err := udp.WriteTo(buffer[:n], peer); err != nil {
					return
				}
			}
		}()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if control != nil {
			if err := control(r.URL.Path); err != nil {
				http.Error(w, "synthetic control failed", 500)
				return
			}
		}
		source, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "source address missing", 500)
			return
		}
		w.Header().Set("X-Source", source)
		if r.URL.Path == "/bulk" {
			body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024+1))
			if err != nil {
				http.Error(w, "read failure", 500)
				return
			}
			if _, err := w.Write(body); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			return
		}
		if _, err := w.Write([]byte(source)); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	})
	return (&http.Server{Addr: ":18080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
}
