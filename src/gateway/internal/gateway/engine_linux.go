package gateway

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/nxyexiong/OuternetAWG-client/shared/awg"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

func OpenEngine(ctx context.Context, c Config, p *profile.Profile) (Session, error) {
	endpoint, err := awg.ResolveIPv4(ctx, p.Host)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	t, err := tun.CreateTUN(c.TunnelInterface, p.MTU)
	if err != nil {
		return nil, fmt.Errorf("create Linux TUN: %w", err)
	}
	return awg.Open(t, p.UAPI(endpoint), netip.AddrPortFrom(endpoint, p.Port))
}
