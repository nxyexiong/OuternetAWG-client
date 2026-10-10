package nas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func WriteStatus(directory string, status Status) (result error) {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".status-")
	if err != nil {
		return fmt.Errorf("create runtime status: %w", err)
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, writeErr := file.Write(data)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write runtime status: %w", err)
	}
	return os.Rename(file.Name(), filepath.Join(directory, "status.json"))
}

func ReadStatus(directory string) (Status, error) {
	file, err := privateFile(filepath.Join(directory, "status.json"))
	if err != nil {
		return Status{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32*1024+1))
	if err != nil || len(data) > 32*1024 {
		return Status{}, errors.New("cannot read bounded NAS status")
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, fmt.Errorf("decode NAS status: %w", err)
	}
	return status, nil
}

func (s Status) Check(now time.Time, requireConnected bool) error {
	if s.UpdatedAt.IsZero() || now.Sub(s.UpdatedAt) > 10*time.Second || s.UpdatedAt.After(now.Add(5*time.Second)) {
		return errors.New("NAS status is stale; the daemon may not be running")
	}
	if requireConnected && (s.State != "connected" || !freshHandshake(s.Traffic.Handshake, now)) {
		return errors.New("NAS VPN has no recent successful server handshake")
	}
	return nil
}
