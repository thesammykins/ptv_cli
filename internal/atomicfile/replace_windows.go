//go:build windows

package atomicfile

import "golang.org/x/sys/windows"

func replace(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MOVEFILE_WRITE_THROUGH flushes the replacement itself. Opening directories
// for fsync is not a portable Windows operation.
func syncDirectory(string) error { return nil }
