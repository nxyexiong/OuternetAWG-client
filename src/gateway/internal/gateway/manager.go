package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

type Traffic struct {
	Handshake time.Time `json:"last_handshake,omitempty"`
	Upload    uint64    `json:"session_upload_bytes"`
	Download  uint64    `json:"session_download_bytes"`
	Endpoint  string    `json:"endpoint,omitempty"`
}

type Session interface {
	Stats() (Traffic, error)
	Close() error
}

type Connector func(context.Context, Config, *profile.Profile) (Session, error)

type Status struct {
	State      string           `json:"state"`
	Desired    bool             `json:"desired"`
	Forwarding bool             `json:"forwarding"`
	Error      string           `json:"error,omitempty"`
	Profile    *profile.Summary `json:"profile,omitempty"`
	DNS        []string         `json:"dns,omitempty"`
	Config     Config           `json:"gateway"`
	Traffic    Traffic          `json:"traffic"`
	RetryAt    *time.Time       `json:"retry_at,omitempty"`
}

type Manager struct {
	mu       sync.Mutex
	config   Config
	store    StateStore
	network  Network
	connect  Connector
	saved    SavedState
	profile  *profile.Profile
	session  Session
	status   Status
	started  time.Time
	retryAt  time.Time
	failures int
	closed   bool
}

func NewManager(ctx context.Context, c Config, store StateStore, network Network, connect Connector) (*Manager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := network.Protect(ctx); err != nil {
		return nil, fmt.Errorf("install forwarding protection: %w", err)
	}
	saved, err := store.LoadState()
	if err != nil {
		return nil, err
	}
	m := &Manager{config: c, store: store, network: network, connect: connect, saved: saved,
		status: Status{State: "disconnected", Config: c, Desired: saved.Desired}}
	if saved.Native != "" {
		if m.profile, err = profile.Parse(saved.Name, saved.Native); err != nil {
			return nil, fmt.Errorf("saved profile: %w", err)
		}
		if err = c.ValidateProfile(m.profile); err != nil {
			return nil, err
		}
		m.describeProfile()
	}
	if saved.Desired {
		m.status.State = "reconnecting"
	}
	return m, nil
}

func (m *Manager) describeProfile() {
	summary := m.profile.Summary()
	m.status.Profile = &summary
	m.status.DNS = nil
	for _, address := range m.profile.DNS {
		m.status.DNS = append(m.status.DNS, address.String())
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	value := m.status
	if value.RetryAt != nil {
		retry := *value.RetryAt
		value.RetryAt = &retry
	}
	value.DNS = append([]string(nil), value.DNS...)
	value.Config.ProtectedSources = append([]string(nil), value.Config.ProtectedSources...)
	return value
}

func (m *Manager) Import(name, content string) error {
	p, err := profile.Parse(name, content)
	if err != nil {
		return err
	}
	if err = m.config.ValidateProfile(p); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.saved.Desired || m.session != nil {
		return errors.New("disconnect before replacing the profile")
	}
	value := SavedState{Version: 1, Name: p.Name, Native: p.Native}
	if err = m.store.SaveState(value); err != nil {
		return fmt.Errorf("save private profile: %w", err)
	}
	m.saved, m.profile = value, p
	m.status.Error, m.status.State = "", "disconnected"
	m.status.Traffic = Traffic{}
	m.describeProfile()
	return nil
}

func (m *Manager) Connect(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("service is stopping")
	}
	if m.profile == nil {
		return errors.New("import a profile first")
	}
	if m.session != nil {
		return nil
	}
	value := m.saved
	value.Desired = true
	if err := m.store.SaveState(value); err != nil {
		return fmt.Errorf("save connection preference: %w", err)
	}
	m.saved, m.status.Desired = value, true
	m.failures = 0
	return m.start(ctx, now)
}

func (m *Manager) start(ctx context.Context, now time.Time) error {
	m.status.State, m.status.Error = "connecting", ""
	m.status.RetryAt = nil
	m.retryAt = time.Time{}
	if err := m.network.Protect(ctx); err != nil {
		return m.failed(ctx, now, err)
	}
	if err := m.network.Check(ctx, m.profile); err != nil {
		return m.failed(ctx, now, err)
	}
	session, err := m.connect(ctx, m.config, m.profile)
	if err != nil {
		return m.failed(ctx, now, err)
	}
	m.session, m.started = session, now
	m.status.Traffic = Traffic{}
	if err = m.network.Configure(ctx, m.profile); err != nil {
		return m.failed(ctx, now, fmt.Errorf("configure Linux networking: %w", err))
	}
	return nil
}

func (m *Manager) stopTunnel(ctx context.Context) error {
	denyErr := m.network.Deny(ctx)
	m.status.Forwarding = false
	var closeErr error
	if m.session != nil {
		closeErr = m.session.Close()
		m.session = nil
	}
	return errors.Join(denyErr, closeErr)
}

func (m *Manager) failed(ctx context.Context, now time.Time, cause error) error {
	err := errors.Join(cause, m.stopTunnel(ctx))
	m.status.Error, m.status.State = err.Error(), "reconnecting"
	m.failures++
	delay := min(5*time.Second*time.Duration(1<<min(m.failures-1, 4)), time.Minute)
	m.retryAt = now.Add(delay + time.Duration(rand.IntN(1000))*time.Millisecond)
	m.status.RetryAt = &m.retryAt
	slog.Error("gateway connection failed; protected forwarding is blocked", "error", err, "retry_at", m.retryAt)
	return err
}

func (m *Manager) Tick(ctx context.Context, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.saved.Desired || now.Before(m.retryAt) {
		return
	}
	if m.session == nil {
		_ = m.start(ctx, now) // start records and logs every failure.
		return
	}
	traffic, err := m.session.Stats()
	if err != nil {
		_ = m.failed(ctx, now, fmt.Errorf("read engine state: %w", err))
		return
	}
	m.status.Traffic = traffic
	fresh := !traffic.Handshake.IsZero() && !traffic.Handshake.After(now.Add(5*time.Second)) && now.Sub(traffic.Handshake) < 3*time.Minute
	if !fresh {
		if m.status.Forwarding {
			if err := m.network.Deny(ctx); err != nil {
				_ = m.failed(ctx, now, err)
				return
			}
			m.status.Forwarding = false
		}
		m.status.State = "waiting"
		if now.Sub(m.started) >= 45*time.Second {
			_ = m.failed(ctx, now, errors.New("no recent server handshake; check connectivity, profile and account quota"))
		}
		return
	}
	if err = m.network.Permit(ctx); err != nil {
		_ = m.failed(ctx, now, fmt.Errorf("renew forwarding protection: %w", err))
		return
	}
	m.status.State, m.status.Error, m.status.Forwarding = "connected", "", true
	m.status.RetryAt = nil
	m.failures = 0
}

func (m *Manager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value := m.saved
	value.Desired = false
	saveErr := m.store.SaveState(value)
	m.saved, m.status.Desired = value, false
	err := errors.Join(saveErr, m.stopTunnel(ctx))
	m.status.State, m.status.RetryAt, m.status.Error = "disconnected", nil, ""
	if err != nil {
		m.status.State, m.status.Error = "error", err.Error()
		if saveErr != nil {
			m.status.Error += "; pause could not be persisted: a later service start may reconnect"
		}
		return errors.New(m.status.Error)
	}
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.stopTunnel(ctx)
}
