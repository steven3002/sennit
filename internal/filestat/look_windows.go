//go:build windows

package filestat

import (
	"encoding/binary"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LookOpens is true here: Windows says which file is at a path only through a
// handle to it.
//
// os.SameFile does not answer this on Windows. It reads a file's id only when
// it is asked to compare, by opening the path then (os/types_windows.go,
// loadFileId), so a stat taken before a rename and compared after it reads the
// id of the file that is at the path now, and finds it the same as itself.
const LookOpens = true

// identity is the serial number of the volume a file is on and the file's
// 128-bit id there. Microsoft's documentation of BY_HANDLE_FILE_INFORMATION
// says of the 64-bit index os.SameFile compares that it "is not guaranteed to
// be unique on ReFS", which is what a Dev Drive is, and points to FILE_ID_INFO
// for the 128-bit id. "In the NTFS file system, a file keeps the same file ID
// until it is deleted", so a file renamed into place has an id of its own: it
// was written while the one it replaced still existed.
type identity struct {
	volume uint64
	file   [16]byte
}

func (a identity) is(b identity) bool { return a == b }

// fileIDInfo is FILE_ID_INFO, which golang.org/x/sys/windows does not define.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// look opens the file for its attributes alone, sharing it for everything, and
// reads its size, the moment it was last written and its id through that one
// handle, so the three describe one file even when another process puts a new
// one at the path at that moment.
func look(path string) (*Info, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "look", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(handle)

	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &data); err != nil {
		return nil, &os.PathError{Op: "look", Path: path, Err: err}
	}
	var id fileIDInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
		// Not every file system answers for the 128-bit id. The 64-bit index
		// identifies a file on NTFS, which is where such a volume would be.
		id.VolumeSerialNumber = uint64(data.VolumeSerialNumber)
		id.FileID = [16]byte{}
		binary.LittleEndian.PutUint32(id.FileID[0:4], data.FileIndexLow)
		binary.LittleEndian.PutUint32(id.FileID[4:8], data.FileIndexHigh)
	}
	return &Info{
		size:    int64(data.FileSizeHigh)<<32 | int64(data.FileSizeLow),
		modTime: time.Unix(0, data.LastWriteTime.Nanoseconds()),
		id:      identity{volume: id.VolumeSerialNumber, file: id.FileID},
	}, nil
}
