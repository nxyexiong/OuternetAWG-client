package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/control"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/firewall"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const AdapterName = "OuternetAWG"

// Populated by the release build after checking the official signed archive.
var WintunSHA256 string

type tunnelSession struct {
	engine      *device.Device
	luid        winipcfg.LUID
	escape      *winipcfg.MibIPforwardRow2
	firewall    bool
	lastRenewal time.Time
}

func VerifyWintun() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "wintun.dll"))
	if err != nil {
		return errors.New("wintun.dll is missing; use the packaged release")
	}
	hash := sha256.Sum256(data)
	if WintunSHA256 == "" || !strings.EqualFold(hex.EncodeToString(hash[:]), WintunSHA256) {
		return errors.New("wintun.dll does not match this release; rebuild using build.ps1")
	}
	return nil
}

func Connect(ctx context.Context, p *profile.Profile) (result control.Session, err error) {
	if err = requireServiceIdentity(); err != nil {
		return nil, err
	}
	if err = VerifyWintun(); err != nil {
		return nil, err
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(resolveCtx, "ip", p.Host)
	if err != nil {
		return nil, errors.New("could not resolve the VPN server before connecting")
	}
	var endpoint netip.Addr
	var physical *winipcfg.MibIPforwardRow2
	for _, candidate := range addresses {
		candidate = candidate.Unmap()
		if !candidate.IsGlobalUnicast() {
			continue
		}
		route, routeErr := bestRoute(candidate)
		if routeErr == nil {
			endpoint, physical = candidate, route
			break
		}
	}
	if physical == nil {
		return nil, errors.New("no usable route to the VPN server; check your network connection")
	}
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagDefault)
	if err != nil {
		return nil, fmt.Errorf("inspect network adapters: %w", err)
	}
	for _, adapter := range adapters {
		if adapter.FriendlyName() == AdapterName {
			return nil, errors.New("an OuternetAWG adapter already exists; stop other client instances before connecting")
		}
	}
	tun.WintunTunnelType = "OuternetAWG"
	t, err := tun.CreateTUN(AdapterName, p.MTU)
	if err != nil {
		return nil, fmt.Errorf("create Wintun adapter: %w", err)
	}
	native, ok := t.(*tun.NativeTun)
	if !ok {
		t.Close()
		return nil, errors.New("unexpected Windows tunnel adapter")
	}
	session := &tunnelSession{luid: winipcfg.LUID(native.LUID())}
	defer func() {
		if err != nil {
			err = errors.Join(err, session.Close())
		}
	}()
	bind := conn.NewDefaultBind()
	var lastEngineError atomic.Int64
	session.engine = device.NewDevice(t, bind, &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf: func(string, ...any) {
			now, last := time.Now().Unix(), lastEngineError.Load()
			if now-last >= 60 && lastEngineError.CompareAndSwap(last, now) {
				slog.Error("AmneziaWG engine reported an error; reconnect or check the imported config (rate limited to once per minute)")
			}
		},
	})
	if err = session.engine.IpcSet(p.UAPI(endpoint)); err != nil {
		return nil, errors.New("AmneziaWG rejected the config; check its keys and obfuscation parameters")
	}
	// Install the dynamic WFP boundary before enabling any tunnel routes.
	if err = firewall.EnableFirewall(uint64(session.luid), false, p.DNS); err != nil {
		return nil, fmt.Errorf("enable tunnel leak protection: %w", err)
	}
	session.firewall = true
	if err = session.configure(ctx, p); err != nil {
		return nil, err
	}
	if err = session.addEscape(endpoint, physical); err != nil {
		return nil, err
	}
	if err = session.engine.Up(); err != nil {
		return nil, errors.New("could not start AmneziaWG")
	}
	if binding, ok := bind.(conn.BindSocketToInterface); ok {
		if endpoint.Is4() {
			err = binding.BindSocketToInterface4(physical.InterfaceIndex, false)
		} else {
			err = binding.BindSocketToInterface6(physical.InterfaceIndex, false)
		}
		if err != nil {
			return nil, fmt.Errorf("bind VPN transport to uplink: %w", err)
		}
	}
	for _, prefix := range p.Routes() {
		nextHop := netip.IPv4Unspecified()
		if prefix.Addr().Is6() {
			nextHop = netip.IPv6Unspecified()
		}
		if err = session.luid.AddRoute(prefix, nextHop, 0); err != nil {
			return nil, fmt.Errorf("install full-tunnel route: %w", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	slog.Info("tunnel started")
	return session, nil
}

func bestRoute(endpoint netip.Addr) (*winipcfg.MibIPforwardRow2, error) {
	family := winipcfg.AddressFamily(windows.AF_INET)
	if endpoint.Is6() {
		family = windows.AF_INET6
	}
	routes, err := winipcfg.GetIPForwardTable2(family)
	if err != nil {
		return nil, err
	}
	var best *winipcfg.MibIPforwardRow2
	var bestCost uint64
	for _, row := range routes {
		prefix := row.DestinationPrefix.Prefix()
		if !prefix.Contains(endpoint) || row.ValidLifetime == 0 {
			continue
		}
		iface, err := row.InterfaceLUID.Interface()
		if err != nil || iface.OperStatus != winipcfg.IfOperStatusUp || iface.Alias() == AdapterName {
			continue
		}
		ip, err := row.InterfaceLUID.IPInterface(family)
		if err != nil {
			continue
		}
		cost := uint64(row.Metric) + uint64(ip.Metric)
		if best == nil || prefix.Bits() > best.DestinationPrefix.Prefix().Bits() ||
			prefix.Bits() == best.DestinationPrefix.Prefix().Bits() && cost < bestCost {
			copy := row
			best, bestCost = &copy, cost
		}
	}
	if best == nil {
		return nil, errors.New("no endpoint route")
	}
	return best, nil
}

func (s *tunnelSession) configure(ctx context.Context, p *profile.Profile) error {
	families := []winipcfg.AddressFamily{windows.AF_INET}
	for _, prefix := range p.Addresses {
		if prefix.Addr().Is6() {
			families = append(families, windows.AF_INET6)
			break
		}
	}
	for _, family := range families {
		row, err := waitInterface(ctx, s.luid, family)
		if err != nil {
			return fmt.Errorf("read tunnel interface: %w", err)
		}
		row.NLMTU, row.Metric, row.UseAutomaticMetric = uint32(p.MTU), 0, false
		row.DadTransmits = 0
		row.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		row.ManagedAddressConfigurationSupported = false
		row.OtherStatefulConfigurationSupported = false
		if err = row.Set(); err != nil {
			return fmt.Errorf("configure tunnel interface: %w", err)
		}
	}
	if err := s.luid.AddIPAddresses(p.Addresses); err != nil {
		return fmt.Errorf("assign tunnel address: %w", err)
	}
	for _, family := range families {
		if err := s.luid.SetDNS(family, p.DNS, nil); err != nil {
			return fmt.Errorf("configure tunnel DNS: %w", err)
		}
	}

	return nil
}

func waitInterface(ctx context.Context, luid winipcfg.LUID, family winipcfg.AddressFamily) (*winipcfg.MibIPInterfaceRow, error) {
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		row, err := luid.IPInterface(family)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, windows.ERROR_NOT_FOUND) {
			return nil, err
		}
		select {
		case <-timeout.Done():
			return nil, errors.New("Windows did not finish initializing the Wintun interface")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *tunnelSession) addEscape(endpoint netip.Addr, physical *winipcfg.MibIPforwardRow2) error {
	destination := netip.PrefixFrom(endpoint, endpoint.BitLen())
	// A pre-existing exact host route belongs to its original owner.
	if physical.DestinationPrefix.Prefix() == destination {
		return nil
	}
	row := &winipcfg.MibIPforwardRow2{}
	row.Init()
	row.InterfaceLUID, row.InterfaceIndex = physical.InterfaceLUID, physical.InterfaceIndex
	row.NextHop = physical.NextHop
	if err := row.DestinationPrefix.SetPrefix(destination); err != nil {
		return err
	}
	row.Metric = 0
	// A process crash cannot leave a permanent server bypass on the physical NIC.
	row.ValidLifetime, row.PreferredLifetime = 90, 90
	if err := row.Create(); err != nil {
		return fmt.Errorf("pin VPN server route: %w", err)
	}
	s.escape, s.lastRenewal = row, time.Now()
	return nil
}

func (s *tunnelSession) Handshake() (time.Time, error) {
	select {
	case <-s.engine.Wait():
		return time.Time{}, errors.New("AmneziaWG stopped")
	default:
	}
	if s.escape != nil && time.Since(s.lastRenewal) >= 20*time.Second {
		row := *s.escape
		row.ValidLifetime, row.PreferredLifetime = 90, 90
		if err := row.Set(); err != nil {
			return time.Time{}, errors.New("VPN server route was lost")
		}
		s.lastRenewal = time.Now()
	}
	text, err := s.engine.IpcGet()
	if err != nil {
		return time.Time{}, errors.New("could not read AmneziaWG handshake state")
	}
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return time.Time{}, errors.New("invalid engine handshake state")
			}
			if n > 0 {
				return time.Unix(n, 0), nil
			}
		}
	}
	return time.Time{}, nil
}

func (s *tunnelSession) Close() error {
	err := releaseTunnel(
		func() {
			if s.engine != nil {
				// Closing our created Wintun adapter removes its addresses, DNS and routes.
				// Do not separately delete Windows-generated interface routes or blank its DNS.
				s.engine.Close()
				s.engine = nil
			}
			s.luid = 0
		},
		func() error {
			if s.escape == nil {
				return nil
			}
			err := s.escape.Delete()
			s.escape = nil
			if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
				return fmt.Errorf("remove VPN server route: %w", err)
			}
			return nil
		},
		func() {
			if s.firewall {
				firewall.DisableFirewall()
				s.firewall = false
			}
		},
	)
	if err != nil {
		slog.Error("tunnel cleanup failed", "error", err)
	} else {
		slog.Info("tunnel disconnected")
	}
	return err
}

func releaseTunnel(closeAdapter func(), removeEndpointRoute func() error, releaseFirewall func()) error {
	// Keep leak protection until the engine, adapter and endpoint exception have been released.
	defer releaseFirewall()
	closeAdapter()
	return removeEndpointRoute()
}
