package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

type engineSession struct {
	engine   *device.Device
	endpoint string
}

func OpenEngine(ctx context.Context, c Config, p *profile.Profile) (Session, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(resolveCtx, "ip4", p.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve the server over the host's normal network: %w", err)
	}
	var endpoint netip.Addr
	for _, address := range addresses {
		address = address.Unmap()
		if address.Is4() && address.IsGlobalUnicast() {
			endpoint = address
			break
		}
	}
	if !endpoint.IsValid() {
		return nil, errors.New("the gateway requires a usable IPv4 server endpoint")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	t, err := tun.CreateTUN(c.TunnelInterface, p.MTU)
	if err != nil {
		return nil, fmt.Errorf("create Linux TUN: %w", err)
	}
	var lastError atomic.Int64
	engine := device.NewDevice(t, conn.NewDefaultBind(), &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf: func(string, ...any) {
			now, previous := time.Now().Unix(), lastError.Load()
			if now-previous >= 60 && lastError.CompareAndSwap(previous, now) {
				slog.Error("AmneziaWG engine reported an error (details suppressed to protect profile keys)")
			}
		},
	})
	if err = engine.IpcSet(p.UAPI(endpoint)); err != nil {
		engine.Close()
		return nil, errors.New("AmneziaWG rejected the profile's keys or protocol settings")
	}
	if err = engine.Up(); err != nil {
		engine.Close()
		return nil, errors.New("AmneziaWG transport could not start")
	}
	return &engineSession{engine: engine, endpoint: netip.AddrPortFrom(endpoint, p.Port).String()}, nil
}

func (s *engineSession) Stats() (Traffic, error) {
	data, err := s.engine.IpcGet()
	if err != nil {
		return Traffic{}, errors.New("AmneziaWG status is unavailable")
	}
	return parseTraffic(data, s.endpoint)
}

func (s *engineSession) Close() error {
	s.engine.Close()
	return nil
}

func parseTraffic(data, endpoint string) (Traffic, error) {
	value := Traffic{Endpoint: endpoint}
	var seconds, nanos int64
	for _, line := range strings.Split(data, "\n") {
		key, text, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		var err error
		switch key {
		case "last_handshake_time_sec":
			seconds, err = strconv.ParseInt(text, 10, 64)
		case "last_handshake_time_nsec":
			nanos, err = strconv.ParseInt(text, 10, 64)
		case "rx_bytes":
			value.Download, err = strconv.ParseUint(text, 10, 64)
		case "tx_bytes":
			value.Upload, err = strconv.ParseUint(text, 10, 64)
		}
		if err != nil {
			return Traffic{}, errors.New("invalid AmneziaWG counters")
		}
	}
	if seconds < 0 || nanos < 0 || nanos >= int64(time.Second) {
		return Traffic{}, errors.New("invalid AmneziaWG handshake timestamp")
	}
	if seconds != 0 {
		value.Handshake = time.Unix(seconds, nanos)
	}
	return value, nil
}
