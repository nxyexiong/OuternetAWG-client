package platform

import (
	"errors"
	"reflect"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

func TestTeardownKeepsFirewallUntilAdapterAndEndpointAreReleased(t *testing.T) {
	for _, routeErr := range []error{nil, errors.New("endpoint deletion failed")} {
		var order []string
		err := releaseTunnel(
			func() { order = append(order, "close adapter") },
			func() error { order = append(order, "remove endpoint"); return routeErr },
			func() { order = append(order, "release firewall") },
		)
		if !reflect.DeepEqual(order, []string{"close adapter", "remove endpoint", "release firewall"}) {
			t.Fatalf("unsafe teardown order: %v", order)
		}
		if !errors.Is(err, routeErr) {
			t.Fatal("teardown must not hide genuine endpoint cleanup failures")
		}
	}
}

func TestCloseUsesAdapterOwnershipInsteadOfFlushingWindowsNetworkTables(t *testing.T) {
	memory := &memoryTun{events: make(chan tun.Event), done: make(chan struct{})}
	engine := device.NewDevice(memory, conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	session := &tunnelSession{engine: engine}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-memory.done:
	default:
		t.Fatal("adapter was not closed")
	}
	select {
	case <-engine.Wait():
	default:
		t.Fatal("engine did not finish stopping")
	}
	if session.engine != nil || session.luid != 0 {
		t.Fatal("closed resources were retained")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("repeated close must be harmless: %v", err)
	}
}
