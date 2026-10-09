package platform

import (
	"bytes"
	"encoding/base64"
	"net/netip"
	"os"
	"sync"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/control"
)

type memoryTun struct {
	events chan tun.Event
	done   chan struct{}
	once   sync.Once
}

func (t *memoryTun) File() *os.File                          { return nil }
func (t *memoryTun) MTU() (int, error)                       { return 1280, nil }
func (t *memoryTun) Name() (string, error)                   { return "in-memory-test-only", nil }
func (t *memoryTun) Events() <-chan tun.Event                { return t.events }
func (t *memoryTun) BatchSize() int                          { return 1 }
func (t *memoryTun) Read([][]byte, []int, int) (int, error)  { <-t.done; return 0, os.ErrClosed }
func (t *memoryTun) Write(bufs [][]byte, _ int) (int, error) { return len(bufs), nil }
func (t *memoryTun) Close() error                            { t.once.Do(func() { close(t.done); close(t.events) }); return nil }

func TestManagementParametersAcceptedByPinnedEngine(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	text := "[Interface]\nPrivateKey = " + key + "\nAddress = 10.110.0.2/32\nDNS = 1.1.1.1\n" +
		"Jc = 4\nJmin = 40\nJmax = 80\nS1 = 16\nS2 = 16\nS3 = 16\nS4 = 16\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\nHeaderProtectionKey = " + key +
		"\n[Peer]\nPublicKey = " + key + "\nEndpoint = 198.51.100.1:51820\nAllowedIPs = 0.0.0.0/0, ::/0"
	p, err := profile.Parse("Synthetic", text)
	if err != nil {
		t.Fatal(err)
	}
	// Never call Up: this test creates no OS adapter, socket, route or WFP filter.
	engine := device.NewDevice(&memoryTun{events: make(chan tun.Event), done: make(chan struct{})},
		conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	defer engine.Close()
	if err := engine.IpcSet(p.UAPI(netip.MustParseAddr("198.51.100.1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.IpcGet(); err != nil {
		t.Fatal(err)
	}
}

func TestDPAPIStoreRoundtrip(t *testing.T) {
	store := Store{Directory: t.TempDir()}
	saved := control.SavedProfile{Name: "Synthetic", Config: "synthetic-private-config"}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Directory + `\profile.dpapi`)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(saved.Config)) {
		t.Fatal("config persisted in clear text")
	}
	got, err := store.Load()
	if err != nil || *got != saved {
		t.Fatalf("roundtrip: %v", err)
	}
	saved.Name = "Replaced"
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || got.Name != "Replaced" {
		t.Fatal("atomic replacement failed")
	}
}
