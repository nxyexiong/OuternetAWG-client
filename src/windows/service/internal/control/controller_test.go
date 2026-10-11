package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
)

type memoryStore struct {
	saved  *SavedProfile
	fail   bool
	routes *routing.State
}

func (s *memoryStore) LoadRouting() (routing.State, error) {
	if s.routes == nil {
		return routing.Default(), nil
	}
	return s.routes.Clone(), nil
}
func (s *memoryStore) SaveRouting(state routing.State) error {
	if s.fail {
		return errors.New("disk full")
	}
	s.routes = &state
	return nil
}
func (s *memoryStore) Load() (*SavedProfile, error) { return s.saved, nil }
func (s *memoryStore) Save(p SavedProfile) error {
	if s.fail {
		return errors.New("disk full")
	}
	s.saved = &p
	return nil
}

type fakeSession struct {
	handshake time.Time
	closed    int
	err       error
}

func (s *fakeSession) Handshake() (time.Time, error) { return s.handshake, s.err }
func (s *fakeSession) Close() error                  { s.closed++; return s.err }
func testConfig() string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.110.0.2/32\nDNS = 1.1.1.1\n[Peer]\nPublicKey = " + key +
		"\nEndpoint = node.example.com:51820\nAllowedIPs = 0.0.0.0/0, ::/0"
}

func TestLifecycleOnlyConnectedAfterHandshake(t *testing.T) {
	store, session := &memoryStore{}, &fakeSession{}
	c, err := New(store, func(context.Context, *profile.Profile, routing.Profile) (Session, error) { return session, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("connect without config")
	}
	if err := c.Import("One", testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Status().State != "connecting" || !c.Status().Active {
		t.Fatal("tunnel setup must not claim a handshake")
	}
	c.Refresh(time.Now().Add(time.Minute))
	if c.Status().State != "waiting" {
		t.Fatal("timeout not visible")
	}
	if err := c.Import("Two", testConfig()); err == nil {
		t.Fatal("replaced active profile")
	}
	session.handshake = time.Now()
	c.Refresh(time.Now())
	if c.Status().State != "connected" {
		t.Fatal("handshake not reflected")
	}
	c.Refresh(time.Now().Add(4 * time.Minute))
	if c.Status().State != "waiting" {
		t.Fatal("stale handshake shown connected")
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c.Disconnect()
	if session.closed != 1 || c.Status().Active {
		t.Fatal("disconnect not idempotent")
	}
	restarted, err := New(store, nil)
	if err != nil || restarted.Status().Active || restarted.Status().Profile.Name != "One" {
		t.Fatal("restart must keep config but not auto-connect")
	}
}

func TestFailedSavePreservesProfile(t *testing.T) {
	store := &memoryStore{}
	c, _ := New(store, nil)
	if err := c.Import("One", testConfig()); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if err := c.Import("Two", testConfig()); err == nil {
		t.Fatal("ignored failed persistence")
	}
	if c.Status().Profile.Name != "One" || store.saved.Name != "One" {
		t.Fatal("mutated profile before persistence")
	}
}

func TestFailuresAreVisibleAndShutdownPreventsReconnect(t *testing.T) {
	store := &memoryStore{}
	c, _ := New(store, func(context.Context, *profile.Profile, routing.Profile) (Session, error) {
		return nil, errors.New("route setup failed")
	})
	c.Import("One", testConfig())
	if c.Connect(context.Background()) == nil || c.Status().State != "error" || c.Status().Active {
		t.Fatal("failed connect looked successful")
	}
	c.Close()
	if c.Connect(context.Background()) == nil || c.Import("Two", testConfig()) == nil {
		t.Fatal("operation accepted after shutdown")
	}
}

func TestConnectionFailureIsLoggedWithoutProfileKeys(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	c, err := New(&memoryStore{}, func(context.Context, *profile.Profile, routing.Profile) (Session, error) {
		return nil, errors.New("Windows service identity is missing")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Import("Synthetic", testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("expected connection failure")
	}
	text := output.String()
	if !strings.Contains(text, "tunnel connection failed") || !strings.Contains(text, "Windows service identity is missing") {
		t.Fatal("connection failure was not logged")
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	if strings.Contains(text, key) || strings.Contains(text, "PrivateKey") {
		t.Fatal("profile key material was logged")
	}
}

func TestIPCFraming(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"command":"status"}`,
		`{"version":2,"command":"status"}`,
		`{"version":1,"command":"status","extra":true}`,
		`{"version":1,"command":"status"}{}`,
	} {
		var b bytes.Buffer
		binary.Write(&b, binary.LittleEndian, uint32(len(body)))
		b.WriteString(body)
		r, err := ReadRequest(&b)
		if body == `{"version":1,"command":"status"}` {
			if err != nil || r.Command != "status" {
				t.Fatal("valid frame failed")
			}
		} else if err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
	for _, size := range []uint32{0, MaxFrame + 1} {
		var b bytes.Buffer
		binary.Write(&b, binary.LittleEndian, size)
		if _, err := ReadRequest(&b); err == nil {
			t.Fatal("unbounded frame")
		}
	}
	var out bytes.Buffer
	if err := WriteResponse(&out, Response{Version: 1, OK: true, Status: Status{State: "disconnected"}}); err != nil {
		t.Fatal(err)
	}
	if int(binary.LittleEndian.Uint32(out.Bytes())) != out.Len()-4 || !strings.Contains(out.String(), `"version":1`) {
		t.Fatal("response framing")
	}
}
