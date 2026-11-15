//go:build windows

package private

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Restrict leaves a file open to the account running this and to nobody
// else.
//
// A file mode means nothing here: Windows keeps one bit of it, read-only, and
// decides who may open a file from its access list, which a new file takes from
// the folder it is made in. So the list is replaced by one entry, full access
// for this account, and marked protected so that nothing is handed down from
// the folder again.
func Restrict(file *os.File) error { return restrictPath(file.Name()) }

// restrictPath does what Restrict does, by name, without opening the file.
func restrictPath(name string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("find the account running this: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(ownerOnly(user.User.Sid))
	if err != nil {
		return fmt.Errorf("build an access list for this account alone: %w", err)
	}
	list, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("build an access list for this account alone: %w", err)
	}
	// By name and not through a handle: a handle opened to write the file does
	// not carry the right to change who may open it.
	err = windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, list, nil)
	if err != nil {
		return fmt.Errorf("limit %s to this account: %w", name, err)
	}
	return nil
}

// ownerOnly is the access list Restrict sets, in the notation Windows prints
// one in: protected from inheritance, with one entry allowing the account full
// access to the file.
func ownerOnly(account *windows.SID) string {
	return "D:P(A;;FA;;;" + account.String() + ")"
}
