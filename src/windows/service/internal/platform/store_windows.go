package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/control"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/routing"
	"golang.org/x/sys/windows"
)

type Store struct{ Directory string }

func SecureDirectory() (string, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "OuternetAWG")
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("data directory must not be a link")
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return "", err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return "", err
	}
	owner, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return "", err
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil); err != nil {
		return "", err
	}
	return path, nil
}

func (s Store) Load() (*control.SavedProfile, error) {
	path := filepath.Join(s.Directory, "profile.dpapi")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > control.MaxFrame {
		return nil, errors.New("invalid protected config size")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("decrypt saved config: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	cleartext := unsafe.Slice(out.Data, out.Size)
	defer clear(cleartext)
	var saved control.SavedProfile
	if json.Unmarshal(cleartext, &saved) != nil {
		return nil, errors.New("invalid saved config")
	}
	return &saved, nil
}

func (s Store) Save(saved control.SavedProfile) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	defer clear(data)
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return s.writeAtomic("profile.dpapi", unsafe.Slice(out.Data, out.Size))
}

func (s Store) LoadRouting() (routing.State, error) {
	f, err := os.Open(filepath.Join(s.Directory, "routing.json"))
	if errors.Is(err, os.ErrNotExist) {
		return routing.Default(), nil
	}
	if err != nil {
		return routing.State{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, control.MaxFrame+1))
	if err != nil {
		return routing.State{}, err
	}
	if len(data) > control.MaxFrame {
		return routing.State{}, errors.New("routing settings file is too large")
	}
	var state routing.State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("read routing settings: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return state, errors.New("trailing routing settings data")
	}
	return state, state.Validate()
}

func (s Store) SaveRouting(state routing.State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.writeAtomic("routing.json", data)
}

func (s Store) writeAtomic(name string, data []byte) error {
	tmp, err := os.CreateTemp(s.Directory, "settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	syncErr, closeErr := tmp.Sync(), tmp.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmp.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(s.Directory, name))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
