package nas

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
	"golang.org/x/sys/unix"
)

func privateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	if err = unix.Fstat(fd, &info); err == nil {
		if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != 0 || info.Mode&0077 != 0 {
			err = errors.New("private file must be regular, root-owned and inaccessible to group/others (chmod 600)")
		}
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func LoadProfile(c Config) (*profile.Profile, error) {
	file, err := privateFile(c.ProfileFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, profile.MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read private profile: %w", err)
	}
	p, err := profile.Parse("NAS", string(data))
	if err != nil {
		return nil, err
	}
	return p, c.ValidateProfile(p)
}

func PrepareRuntime(directory string) (*os.File, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("the NAS client must run as root with NET_ADMIN")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	var info unix.Stat_t
	if err := unix.Lstat(directory, &info); err != nil {
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != 0 || info.Mode&0077 != 0 {
		return nil, errors.New("runtime directory must be root-owned, mode 0700, and not a symlink")
	}
	fd, err := unix.Open(filepath.Join(directory, "owner.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.Join(errors.New("another NAS client owns this runtime directory"), unix.Close(fd))
	}
	return os.NewFile(uintptr(fd), "owner.lock"), nil
}
