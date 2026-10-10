package nas

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/nxyexiong/OuternetAWG-client/shared/awg"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"golang.org/x/sys/unix"
)

type engineSession struct {
	*awg.Session
	filter *endpointTUN
}

func (s *engineSession) Dropped() uint64 { return s.filter.dropped.Load() }

func Open(ctx context.Context, c Config, p *profile.Profile) (Session, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := c.ValidateProfile(p); err != nil {
		return nil, err
	}
	endpoint, err := awg.ResolveIPv4(ctx, p.Host)
	if err != nil {
		return nil, err
	}
	uapi, err := p.SubnetUAPI(endpoint, c.Subnet)
	if err != nil {
		return nil, err
	}
	if err = checkNetwork(ctx, c, systemIP); err != nil {
		return nil, err
	}
	t, err := exclusiveTUN(c.Interface, p.MTU)
	if err != nil {
		return nil, fmt.Errorf("create exclusive TUN (requires /dev/net/tun and NET_ADMIN): %w", err)
	}
	filter := &endpointTUN{Device: t, subnet: c.Subnet, address: p.Addresses[0].Addr()}
	engine, err := awg.Open(filter, uapi, netip.AddrPortFrom(endpoint, p.Port))
	if err != nil {
		return nil, err
	}
	if err = configureNetwork(ctx, c, p, systemIP); err != nil {
		return nil, errors.Join(err, engine.Close())
	}
	return &engineSession{Session: engine, filter: filter}, nil
}

func exclusiveTUN(name string, mtu int) (tun.Device, error) {
	request, err := unix.NewIfreq(name)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	request.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI | unix.IFF_VNET_HDR | unix.IFF_TUN_EXCL)
	if err = unix.IoctlIfreq(fd, unix.TUNSETIFF, request); err != nil {
		return nil, errors.Join(err, unix.Close(fd))
	}
	file := os.NewFile(uintptr(fd), "/dev/net/tun")
	device, err := tun.CreateTUNFromFile(file, mtu)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return device, nil
}
