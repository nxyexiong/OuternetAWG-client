package nas

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/shared/awg"
	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

type Session interface {
	Stats() (awg.Traffic, error)
	Done() <-chan struct{}
	Dropped() uint64
	Close() error
}

type Status struct {
	State     string      `json:"state"`
	Address   string      `json:"address"`
	Subnet    string      `json:"subnet"`
	Interface string      `json:"interface"`
	UpdatedAt time.Time   `json:"updated_at"`
	Traffic   awg.Traffic `json:"traffic"`
	Dropped   uint64      `json:"dropped_packets"`
	Error     string      `json:"error,omitempty"`
}

type Client struct {
	Config  Config
	Profile *profile.Profile
	Open    func(context.Context, Config, *profile.Profile) (Session, error)
	Publish func(Status) error
}

func freshHandshake(handshake, now time.Time) bool {
	return !handshake.IsZero() && !handshake.After(now.Add(5*time.Second)) && now.Sub(handshake) < 3*time.Minute
}

func connectionState(traffic awg.Traffic, started, now time.Time) (string, error) {
	if freshHandshake(traffic.Handshake, now) {
		return "connected", nil
	}
	if now.Sub(started) >= 45*time.Second {
		return "reconnecting", errors.New("no recent server handshake; check connectivity, profile and account quota")
	}
	return "waiting", nil
}

func retryDelay(failures int) time.Duration {
	return min(5*time.Second*time.Duration(1<<min(max(failures-1, 0), 4)), time.Minute)
}

func (c *Client) Run(ctx context.Context) (result error) {
	status := Status{Address: c.Profile.Addresses[0].Addr().String(), Subnet: c.Config.Subnet.String(), Interface: c.Config.Interface}
	publish := func(state string) error {
		status.State, status.UpdatedAt = state, time.Now().UTC()
		return c.Publish(status)
	}
	defer func() {
		status.Error = ""
		result = errors.Join(result, publish("stopped"))
	}()
	failures := 0
	for ctx.Err() == nil {
		status.Error, status.Traffic, status.Dropped = "", awg.Traffic{}, 0
		if err := publish("connecting"); err != nil {
			return err
		}
		session, err := c.Open(ctx, c.Config, c.Profile)
		connected := false
		if err == nil {
			var fatal bool
			connected, fatal, err = c.monitor(ctx, session, &status)
			closeErr := session.Close()
			if fatal || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			failures = 0
		}
		failures++
		delay := retryDelay(failures) + time.Duration(rand.IntN(1000))*time.Millisecond
		status.Error = err.Error()
		slog.Warn("NAS VPN disconnected; retrying", "error", err, "delay", delay)
		if err := publish("reconnecting"); err != nil {
			return err
		}
		// Continue refreshing status while waiting, rather than leaving a stale
		// file that cannot distinguish backoff from a dead daemon.
		timer, tick := time.NewTimer(delay), time.NewTicker(2*time.Second)
	wait:
		for {
			select {
			case <-ctx.Done():
				break wait
			case <-timer.C:
				break wait
			case <-tick.C:
				if err := publish("reconnecting"); err != nil {
					timer.Stop()
					tick.Stop()
					return err
				}
			}
		}
		timer.Stop()
		tick.Stop()
	}
	return nil
}

func (c *Client) monitor(ctx context.Context, session Session, status *Status) (connected, fatal bool, result error) {
	started := time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return connected, false, nil
		case <-session.Done():
			return connected, false, errors.New("AmneziaWG tunnel closed unexpectedly")
		case <-tick.C:
			traffic, err := session.Stats()
			if err != nil {
				return connected, false, err
			}
			now := time.Now()
			state, err := connectionState(traffic, started, now)
			if err != nil {
				return connected, false, err
			}
			if state == "connected" && !connected {
				connected = true
				slog.Info("Connected", "NAS_VPN_address", status.Address, "interface", status.Interface, "subnet", status.Subnet)
			}
			status.State, status.UpdatedAt, status.Traffic, status.Dropped = state, now.UTC(), traffic, session.Dropped()
			if err := c.Publish(*status); err != nil {
				return connected, true, err
			}
		}
	}
}
