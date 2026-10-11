package control

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
)

func TestRoutingPersistenceSnapshotAndActiveLock(t *testing.T) {
	store := &memoryStore{}
	var used routing.Profile
	c, err := New(store, func(_ context.Context, _ *profile.Profile, p routing.Profile) (Session, error) {
		used = p
		return &fakeSession{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state := routing.Default()
	id := strings.Repeat("a", 32)
	state.Profiles = []routing.Profile{{ID: id, Name: "LAN", Base: routing.VPN, Rules: []routing.Rule{{Destination: "192.168.0.0/16", Action: routing.Direct}}}}
	state.Selected = id
	if err = c.SaveRouting(state); err != nil {
		t.Fatal(err)
	}
	state.Profiles[0].Name = "changed outside controller"
	if c.Status().Routing.Profiles[0].Name != "LAN" {
		t.Fatal("state ownership lost")
	}
	restarted, err := New(store, nil)
	if err != nil || restarted.Status().Routing.Selected != id {
		t.Fatal("routing not persisted", err)
	}
	if err = c.Import("Test", testConfig()); err != nil {
		t.Fatal(err)
	}
	if c.Status().Routing.Selected != id {
		t.Fatal("import reset independent routing")
	}
	if err = c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if used.ID != id {
		t.Fatal("connector did not receive selected profile")
	}
	if c.SaveRouting(routing.Default()) == nil {
		t.Fatal("changed live routing")
	}
	status := c.Status()
	status.Routing.Profiles[0].Rules[0].Action = routing.VPN
	if used.Rules[0].Action != routing.Direct {
		t.Fatal("active policy was mutable through status")
	}
	if err = c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	before := c.Status().Routing
	store.fail = true
	if c.SaveRouting(routing.Default()) == nil || !reflect.DeepEqual(before, c.Status().Routing) {
		t.Fatal("failed save changed routing")
	}
	store.fail = false
	if err = c.SaveRouting(routing.Default()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if c.SaveRouting(before) == nil {
		t.Fatal("save after shutdown accepted")
	}
}

type invalidRoutingStore struct{ memoryStore }

func (*invalidRoutingStore) LoadRouting() (routing.State, error) {
	return routing.State{}, errors.New("corrupt routing file")
}

func TestCorruptRoutingDoesNotSilentlyUseVPN(t *testing.T) {
	if _, err := New(&invalidRoutingStore{}, nil); err == nil {
		t.Fatal("ignored invalid persistence")
	}
}
