//go:build !linux

package gateway

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

func RequireLinuxRoot() error       { return errors.New("the gateway requires Linux") }
func Lock(string) (*os.File, error) { return nil, RequireLinuxRoot() }
func peerRoot(*net.UnixConn) error  { return RequireLinuxRoot() }
func OpenEngine(context.Context, Config, *profile.Profile) (Session, error) {
	return nil, RequireLinuxRoot()
}
func syncDirectory(string) error { return nil }
func checkPrivate(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("expected a regular private file or directory")
	}
	return nil
}
