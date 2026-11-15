//go:build windows

package privatetest

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OnlyThisAccountMayOpen fails the test unless the file's access list is its
// own and names one account, the one running the test.
//
// A file mode says nothing about this on Windows, where every file a test makes
// reads as 0666. What is checked is what Windows decides access by: that the
// list is protected, so the folder hands nothing down into it, and that its
// only entry allows this account. What is not checked is anything an access
// list cannot stop: an administrator can take ownership of any file, and so can
// whoever holds the disk.
func OnlyThisAccountMayOpen(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("find the account running the test: %v", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read the file's access list: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read the file's access list: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("the file takes access from its folder (%s): it is a secret", descriptor)
	}
	list, _, err := descriptor.DACL()
	if err != nil || list == nil {
		t.Fatalf("the file has no access list (%v), which leaves it open to everyone", err)
	}
	if list.AceCount != 1 {
		t.Errorf("the file's access list has %d entries (%s), want one, for this account: it is a secret",
			list.AceCount, descriptor)
	}
	for i := range uint32(list.AceCount) {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(list, i, &entry); err != nil {
			t.Fatalf("read entry %d of the file's access list: %v", i, err)
		}
		account := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
		if entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !account.Equals(user.User.Sid) {
			t.Errorf("the file's access list names %s (%s), want only this account, %s: it is a secret",
				account, descriptor, user.User.Sid)
		}
	}
}

// OpenToEveryone makes a file any account on the machine can read, which is
// what a file must not stay once a secret is in it.
func OpenToEveryone(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("make a file anyone can read: %v", err)
	}
	// WD is Everyone, and FA is every right there is to a file.
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;WD)")
	if err != nil {
		t.Fatalf("build an access list that lets everyone in: %v", err)
	}
	list, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("build an access list that lets everyone in: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, list, nil); err != nil {
		t.Fatalf("make a file anyone can read: %v", err)
	}
}

// OnlyThisAccountMayEnter fails the test unless the folder's access list is
// its own and names this account alone. A folder and a file are checked the
// same way on Windows.
func OnlyThisAccountMayEnter(t *testing.T, dir string) {
	t.Helper()
	OnlyThisAccountMayOpen(t, dir)
}

// OnlyThisAccountCanRead fails the test unless every entry that allows
// anything in the file's access list names this account. Unlike
// OnlyThisAccountMayOpen it accepts a list the file inherited from its folder.
func OnlyThisAccountCanRead(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("find the account running the test: %v", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read the access list of %s: %v", path, err)
	}
	list, _, err := descriptor.DACL()
	if err != nil || list == nil {
		t.Fatalf("%s has no access list (%v), which leaves it open to everyone", path, err)
	}
	for i := range uint32(list.AceCount) {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(list, i, &entry); err != nil {
			t.Fatalf("read entry %d of the access list of %s: %v", i, path, err)
		}
		account := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
		if entry.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && !account.Equals(user.User.Sid) {
			t.Errorf("%s lets %s in (%s), want only this account, %s", path, account, descriptor, user.User.Sid)
		}
	}
}

// OpenFolderToEveryone makes a folder whose access list lets every account
// in, and hands that to everything made inside it.
func OpenFolderToEveryone(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make a folder anyone can enter: %v", err)
	}
	// WD is Everyone. OI and CI pass the entry to what is made inside.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatalf("build an access list that lets everyone in: %v", err)
	}
	list, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("build an access list that lets everyone in: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, list, nil); err != nil {
		t.Fatalf("make a folder anyone can enter: %v", err)
	}
}

// WidenToEveryone lets every account read a file that is already there,
// leaving what is in it alone, the way a file in a shared folder starts.
func WidenToEveryone(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatalf("build an access list that lets everyone in: %v", err)
	}
	list, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("build an access list that lets everyone in: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, list, nil); err != nil {
		t.Fatalf("let every account read %s: %v", path, err)
	}
}
