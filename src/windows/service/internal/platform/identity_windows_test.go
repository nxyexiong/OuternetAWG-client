package platform

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWFPRequiresPerServiceIdentityNotJustLocalSystem(t *testing.T) {
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	service, err := windows.StringToSid("S-1-5-80-1-2-3-4-5")
	if err != nil {
		t.Fatal(err)
	}
	enabledOwner := uint32(windows.SE_GROUP_ENABLED | windows.SE_GROUP_ENABLED_BY_DEFAULT | windows.SE_GROUP_OWNER)
	for _, test := range []struct {
		name   string
		groups []windows.SIDAndAttributes
		want   bool
	}{
		{"no groups", nil, false},
		{"LocalSystem only", []windows.SIDAndAttributes{{Sid: system, Attributes: enabledOwner}}, false},
		{"disabled service SID", []windows.SIDAndAttributes{{Sid: service}}, false},
		{"non-owner service SID", []windows.SIDAndAttributes{{Sid: service, Attributes: windows.SE_GROUP_ENABLED}}, false},
		{"missing SID", []windows.SIDAndAttributes{{Attributes: enabledOwner}}, false},
		{"enabled service SID", []windows.SIDAndAttributes{{Sid: system, Attributes: enabledOwner}, {Sid: service, Attributes: enabledOwner}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := hasServiceIdentity(test.groups); got != test.want {
				t.Fatalf("hasServiceIdentity = %v, want %v", got, test.want)
			}
		})
	}
}
