//go:build windows

package private

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// restrictFolder replaces the folder's access list with one entry, full access
// for this account, inherited by every file and folder made inside it, and
// marks it protected so nothing is handed down from the folder above.
func restrictFolder(dir string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("find the account running this: %w", err)
	}
	// OI and CI are what make the entry pass to files and folders made inside.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return fmt.Errorf("build an access list for this account alone: %w", err)
	}
	list, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("build an access list for this account alone: %w", err)
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, list, nil)
}

// readAccess is any right that lets an account read a file or list a folder.
const readAccess = windows.FILE_READ_DATA | windows.GENERIC_READ | windows.GENERIC_ALL

func readers(dir string) (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("find the account running this: %w", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	list, _, err := descriptor.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && list == nil):
		return "every account on this machine, since it has no access list", nil
	case err != nil:
		return "", fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	for i := range uint32(list.AceCount) {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(list, i, &entry); err != nil {
			return "", fmt.Errorf("read entry %d of the access list of %s: %w", i, dir, err)
		}
		if entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || entry.Mask&readAccess == 0 {
			continue
		}
		// An entry only for folders made inside reaches neither this folder nor
		// any file in it.
		flags := entry.Header.AceFlags
		if flags&windows.INHERIT_ONLY_ACE != 0 && flags&windows.OBJECT_INHERIT_ACE == 0 {
			continue
		}
		account := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
		if trusted(account, user.User.Sid) {
			continue
		}
		return name(account), nil
	}
	return "", nil
}

// trusted is this account, and the ones every folder in a person's own profile
// already lets in: the system itself, the administrators, and the stand-ins
// Windows replaces with the account that owns or makes a file.
func trusted(account, user *windows.SID) bool {
	if account.Equals(user) {
		return true
	}
	for _, known := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid,
		windows.WinBuiltinAdministratorsSid,
		windows.WinCreatorOwnerSid,
		windows.WinCreatorOwnerRightsSid,
	} {
		if account.IsWellKnown(known) {
			return true
		}
	}
	return false
}

// name is an account as Windows shows it, such as BUILTIN\Users, or its
// identifier when it has no name here.
func name(account *windows.SID) string {
	user, domain, _, err := account.LookupAccount("")
	switch {
	case err != nil:
		return account.String()
	case domain == "":
		return user
	default:
		return domain + `\` + user
	}
}
