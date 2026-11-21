//go:build windows

package romanize

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mmapReadOnly maps path read-only into the process address space.
// Keeping the file handle open until unmap prevents truncation while mapped on Windows.
func mmapReadOnly(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, nil, err
	}
	size := fi.Size()
	if size <= 0 {
		_ = f.Close()

		return nil, nil, fmt.Errorf("romanize: empty mmap file %s", path)
	}

	hMap, err := windows.CreateFileMapping(
		windows.Handle(f.Fd()),
		nil,
		windows.PAGE_READONLY,
		0,
		0,
		nil,
	)
	if err != nil {
		_ = f.Close()

		return nil, nil, fmt.Errorf("romanize: CreateFileMapping: %w", err)
	}

	addr, err := windows.MapViewOfFile(hMap, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		_ = windows.CloseHandle(hMap)
		_ = f.Close()

		return nil, nil, fmt.Errorf("romanize: MapViewOfFile: %w", err)
	}

	// MapViewOfFile returns an OS virtual address outside the Go heap.
	// Passing it through unsafe.Add avoids a direct uintptr-to-Pointer conversion.
	base := (*byte)(unsafe.Add(nil, addr))
	data := unsafe.Slice(base, int(size))
	closeFn := func() error {
		e1 := windows.UnmapViewOfFile(addr)
		e2 := windows.CloseHandle(hMap)
		e3 := f.Close()
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}

		return e3
	}

	return data, closeFn, nil
}
