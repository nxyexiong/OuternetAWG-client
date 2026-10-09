package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/profile"
)

type SavedProfile struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}

type Store interface {
	Load() (*SavedProfile, error)
	Save(SavedProfile) error
}

type Session interface {
	Handshake() (time.Time, error)
	Close() error
}

type Connector func(context.Context, *profile.Profile) (Session, error)

type Status struct {
	State     string           `json:"state"`
	Active    bool             `json:"active"`
	Message   string           `json:"message"`
	Profile   *profile.Summary `json:"profile,omitempty"`
	Handshake *time.Time       `json:"handshake,omitempty"`
}

type Controller struct {
	mu      sync.Mutex
	store   Store
	connect Connector
	profile *profile.Profile
	session Session
	started time.Time
	status  Status
	stopped bool
}

func New(store Store, connect Connector) (*Controller, error) {
	c := &Controller{store: store, connect: connect, status: Status{State: "disconnected", Message: "Import a config to get started."}}
	saved, err := store.Load()
	if err != nil {
		return nil, err
	}
	if saved != nil {
		c.profile, err = profile.Parse(saved.Name, saved.Config)
		if err != nil {
			return nil, errors.New("saved config is invalid; repair or remove it before starting the service")
		}
		summary := c.profile.Summary()
		c.status.Profile = &summary
		c.status.Message = "Ready to connect."
	}
	return c, nil
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Controller) Import(name, content string) error {
	p, err := profile.Parse(name, content)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return errors.New("service is stopping")
	}
	if c.session != nil {
		return errors.New("disconnect before replacing the config")
	}
	if err := c.store.Save(SavedProfile{Name: name, Config: p.Native}); err != nil {
		slog.Error("persist protected config", "error", err)
		return errors.New("could not securely save config; the previous config is unchanged")
	}
	c.profile = p
	summary := p.Summary()
	c.status = Status{State: "disconnected", Message: "Ready to connect.", Profile: &summary}
	return nil
}

func (c *Controller) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return errors.New("service is stopping")
	}
	if c.session != nil {
		return errors.New("a tunnel is already active")
	}
	if c.profile == nil {
		return errors.New("import a config first")
	}
	session, err := c.connect(ctx, c.profile)
	if err != nil {
		slog.Error("tunnel connection failed", "error", err)
		c.status.State, c.status.Message = "error", err.Error()
		return err
	}
	c.session, c.started = session, time.Now()
	c.status.State, c.status.Active = "connecting", true
	c.status.Message = "Tunnel is ready. Waiting for the server handshake..."
	return nil
}

func (c *Controller) Refresh(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return
	}
	handshake, err := c.session.Handshake()
	if err != nil {
		c.status.State, c.status.Message = "error", err.Error()+" Disconnect and reconnect. Traffic remains blocked outside the tunnel."
		return
	}
	if handshake.IsZero() {
		if now.Sub(c.started) >= 30*time.Second {
			c.status.State = "waiting"
			c.status.Message = "No server response yet. Check the config, network and account quota, or disconnect."
		}
		return
	}
	c.status.Handshake = &handshake
	if now.Sub(handshake) > 3*time.Minute {
		c.status.State, c.status.Message = "waiting", "Waiting for a fresh handshake. Traffic remains inside the tunnel."
	} else {
		c.status.State, c.status.Message = "connected", "Connected. Your traffic is routed through Outernet."
	}
}

func (c *Controller) disconnectLocked() error {
	if c.session != nil {
		err := c.session.Close()
		c.session = nil
		c.status.Active, c.status.Handshake = false, nil
		if err != nil {
			c.status.State, c.status.Message = "error", "Tunnel closed, but network cleanup failed: "+err.Error()
			return err
		}
	}
	c.status.State, c.status.Message = "disconnected", "Disconnected. Your normal network is restored."
	return nil
}

func (c *Controller) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disconnectLocked()
}

func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	return c.disconnectLocked()
}
