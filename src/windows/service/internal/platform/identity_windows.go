package platform

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func requireServiceIdentity() error {
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		return fmt.Errorf("read Windows service identity: %w", err)
	}
	if !hasServiceIdentity(groups.AllGroups()) {
		return errors.New("Windows service identity required by tunnel leak protection is missing; run the latest OuternetAWG installer to repair the service, then reopen the app")
	}
	return nil
}

func hasServiceIdentity(groups []windows.SIDAndAttributes) bool {
	// The pinned WFP helper uses an enabled, owner-capable per-service SID to permit transport traffic.
	const required = windows.SE_GROUP_ENABLED | windows.SE_GROUP_ENABLED_BY_DEFAULT | windows.SE_GROUP_OWNER
	for _, group := range groups {
		sid := group.Sid
		if group.Attributes == required && sid != nil && sid.IsValid() &&
			sid.IdentifierAuthority() == windows.SECURITY_NT_AUTHORITY &&
			sid.SubAuthorityCount() >= 6 && sid.SubAuthority(0) == 80 {
			return true
		}
	}
	return false
}
