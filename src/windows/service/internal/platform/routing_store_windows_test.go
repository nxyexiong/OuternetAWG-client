package platform

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
)

func TestTunnelAddressesDoNotCreateImplicitSubnetRoutes(t *testing.T) {
	input := []netip.Prefix{netip.MustParsePrefix("10.110.0.2/16"), netip.MustParsePrefix("fd00::2/64")}
	got := hostAddresses(input)
	want := []netip.Prefix{netip.MustParsePrefix("10.110.0.2/32"), netip.MustParsePrefix("fd00::2/128")}
	if !reflect.DeepEqual(got, want) || input[0].Bits() != 16 {
		t.Fatal(got, input)
	}
}
func TestRoutingFilePersistence(t *testing.T) {
	store := Store{Directory: t.TempDir()}
	state, err := store.LoadRouting()
	if err != nil || state.Selected != "vpn" {
		t.Fatal(state, err)
	}
	state.Selected = strings.Repeat("b", 32)
	state.Profiles = []routing.Profile{{ID: state.Selected, Name: "NAS", Base: routing.Direct, Rules: []routing.Rule{{Destination: "10.110.0.0/16", Action: routing.VPN}}}}
	if err = store.SaveRouting(state); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadRouting()
	if err != nil || !reflect.DeepEqual(got, state) {
		t.Fatal(got, err)
	}
	files, err := os.ReadDir(store.Directory)
	if err != nil || len(files) != 1 || files[0].Name() != "routing.json" {
		t.Fatal("temporary file leaked", files, err)
	}
	path := filepath.Join(store.Directory, "routing.json")
	for _, content := range []string{"null", "{}", "{", `{"version":2}`, `{"version":1,"selected":"vpn","unknown":true}`, `{"version":1,"selected":"missing","profiles":[]}`, `{"version":1,"selected":"vpn"}{}`} {
		if err = os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = store.LoadRouting(); err == nil {
			t.Fatalf("accepted %s", content)
		}
		bytes, err := os.ReadFile(path)
		if err != nil || string(bytes) != content {
			t.Fatal("overwrote corrupt routing file")
		}
	}
}
