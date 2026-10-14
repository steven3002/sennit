//go:build windows

package main

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// onlyThisAccountMayOpen fails the test unless the file's access list is its
// own and names one account, the one running the test.
//
// A file mode says nothing about this on Windows, where every file a test makes
// reads as 0666. What is checked is what Windows decides access by: that the
// list is protected, so the folder hands nothing down into it, and that its
// only entry allows this account. What is not checked is anything an access
// list cannot stop: an administrator can take ownership of any file, and so can
// whoever holds the disk.
func onlyThisAccountMayOpen(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("find the account running the test: %v", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read the key file's access list: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read the key file's access list: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("the key file takes access from its folder (%s): it is a secret", descriptor)
	}
	list, _, err := descriptor.DACL()
	if err != nil || list == nil {
		t.Fatalf("the key file has no access list (%v), which leaves it open to everyone", err)
	}
	if list.AceCount != 1 {
		t.Errorf("the key file's access list has %d entries (%s), want one, for this account: it is a secret",
			list.AceCount, descriptor)
	}
	for i := range uint32(list.AceCount) {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(list, i, &entry); err != nil {
			t.Fatalf("read entry %d of the key file's access list: %v", i, err)
		}
		account := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
		if entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !account.Equals(user.User.Sid) {
			t.Errorf("the key file's access list names %s (%s), want only this account, %s: it is a secret",
				account, descriptor, user.User.Sid)
		}
	}
}

// openToEveryone makes a file any account on the machine can read, which is
// what a key file must not stay once the key is in it.
func openToEveryone(t *testing.T, path string) {
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
