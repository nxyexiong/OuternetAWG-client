package main

import (
	"strings"
	"testing"
)

func TestBuildIdentityUsesInjectedClientVersion(t *testing.T) {
	previous := ClientVersion
	defer func() { ClientVersion = previous }()
	ClientVersion = "3.4.5"
	if got := buildIdentity(); !strings.HasPrefix(got, "OuternetAWG Windows service 3.4.5;") {
		t.Fatalf("client version was not included in build identity: %s", got)
	}
}
