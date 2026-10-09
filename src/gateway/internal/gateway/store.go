package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type SavedState struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Native  string `json:"native"`
	Desired bool   `json:"desired"`
}

type StateStore interface {
	LoadState() (SavedState, error)
	SaveState(SavedState) error
}

type Store struct{ Directory string }

func (s Store) Init() error { return privateDirectory(s.Directory) }

func (s Store) LoadConfig() (Config, error) {
	data, err := s.read("gateway.json", MaxConfigSize)
	if err != nil {
		return Config{}, fmt.Errorf("read saved gateway settings: %w", err)
	}
	return DecodeConfig(data)
}

func (s Store) SaveConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return s.write("gateway.json", c)
}

func (s Store) LoadState() (SavedState, error) {
	value := SavedState{Version: 1}
	data, err := s.read("state.json", MaxMessageSize)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, fmt.Errorf("read saved state: %w", err)
	}
	if err = decodeJSON(data, &value); err != nil || value.Version != 1 {
		return SavedState{}, errors.New("saved state is invalid; it has not been overwritten")
	}
	if value.Native == "" && (value.Desired || value.Name != "") {
		return SavedState{}, errors.New("saved state has no profile")
	}
	return value, nil
}

func (s Store) SaveState(value SavedState) error {
	value.Version = 1
	return s.write("state.json", value)
}

func (s Store) read(name string, limit int64) ([]byte, error) {
	if err := checkPrivate(s.Directory, true); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Directory, name)
	if err := checkPrivate(path, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("saved file exceeds its size limit")
	}
	return b, nil
}

func (s Store) write(name string, value any) (result error) {
	if err := s.Init(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.Directory, name)
	if err = checkPrivate(path, false); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(s.Directory, ".pending-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() {
		if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("remove temporary private file: %w", err))
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	return syncDirectory(s.Directory)
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return checkPrivate(path, true)
}
