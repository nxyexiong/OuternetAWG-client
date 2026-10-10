package awg

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
)

type Traffic struct {
	Handshake time.Time `json:"last_handshake,omitempty"`
	Upload    uint64    `json:"session_upload_bytes"`
	Download  uint64    `json:"session_download_bytes"`
	Endpoint  string    `json:"endpoint,omitempty"`
}

type Session struct {
	engine   *device.Device
	endpoint string
}

func ResolveIPv4(ctx context.Context, host string) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve the server over the host's normal network: %w", err)
	}
	for _, address := range addresses {
		address = address.Unmap()
		if address.Is4() && address.IsGlobalUnicast() {
			return address, nil
		}
	}
	return netip.Addr{}, errors.New("a usable IPv4 server endpoint is required")
}

// Open takes ownership of t, including on failure. Raw engine errors may contain
// key material, so only rate-limited, redacted errors leave this package.
func Open(t tun.Device, uapi string, endpoint netip.AddrPort) (*Session, error) {
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
	if err := engine.IpcSet(uapi); err != nil {
		engine.Close()
		return nil, errors.New("AmneziaWG rejected the profile's keys or protocol settings")
	}
	if err := engine.Up(); err != nil {
		engine.Close()
		return nil, errors.New("AmneziaWG transport could not start")
	}
	return &Session{engine: engine, endpoint: endpoint.String()}, nil
}

func (s *Session) Stats() (Traffic, error) {
	data, err := s.engine.IpcGet()
	if err != nil {
		return Traffic{}, errors.New("AmneziaWG status is unavailable")
	}
	return parseTraffic(data, s.endpoint)
}

func (s *Session) Done() <-chan struct{} { return s.engine.Wait() }

func (s *Session) Close() error {
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
