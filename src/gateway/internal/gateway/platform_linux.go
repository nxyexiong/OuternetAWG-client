package gateway

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func RequireLinuxRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("run this command as root (sudo)")
	}
	return os.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
}

func checkPrivate(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if directory != info.IsDir() || !directory && !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%s must be a private, non-symlink %s", path, map[bool]string{true: "directory", false: "file"}[directory])
	}
	var stat unix.Stat_t
	if err = unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s has an unexpected owner", path)
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func Lock(directory string) (*os.File, error) {
	if err := privateDirectory(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "owner.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := f.Close()
		return nil, errors.Join(errors.New("gateway is running or another command owns its network state; stop the service first"), closeErr)
	}
	return f, nil
}

func peerRoot(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Ucred
	var credentialErr error
	err = raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err = errors.Join(err, credentialErr); err != nil {
		return err
	}
	if credential == nil || credential.Uid != 0 {
		return errors.New("control socket peer is not root")
	}
	return nil
}
