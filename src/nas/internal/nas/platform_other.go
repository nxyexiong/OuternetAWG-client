//go:build !linux

package nas

import (
	"context"
	"errors"
	"os"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

var errLinux = errors.New("NAS networking requires Linux; use the supplied Docker image")

func Open(context.Context, Config, *profile.Profile) (Session, error) { return nil, errLinux }
func LoadProfile(Config) (*profile.Profile, error)                    { return nil, errLinux }
func PrepareRuntime(string) (*os.File, error)                         { return nil, errLinux }
func privateFile(string) (*os.File, error)                            { return nil, errLinux }
