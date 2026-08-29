//go:build windows

package managerauth

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureDirectory(path string) error {
	return setUserOnlyACL(path, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

func validateTokenPermissions(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect manager token %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("manager token %s is not a regular file", path)
	}

	userSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current user for manager token: %w", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect manager token ACL %s: %w", path, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("manager token %s has no private ACL", path)
	}
	foundUserAccess := false
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return fmt.Errorf("inspect manager token ACL %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.Equals(userSID) {
			return fmt.Errorf("manager token %s ACL grants access to another identity", path)
		}
		foundUserAccess = true
	}
	if !foundUserAccess {
		return fmt.Errorf("manager token %s ACL does not grant access to the current user", path)
	}
	return nil
}

func setUserOnlyACL(path string, inheritance uint32) error {
	userSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current user for manager token: %w", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(userSID),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build manager token ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("secure manager token path %s: %w", path, err)
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}
