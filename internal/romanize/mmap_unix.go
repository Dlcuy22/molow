//go:build unix

package romanize

import (
	"fmt"
	"os"
	"syscall"
)

// mmapReadOnly maps path read-only into the process address space.
func mmapReadOnly(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	size := fi.Size()
	if size <= 0 {
		return nil, nil, fmt.Errorf("romanize: empty mmap file %s", path)
	}

	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("romanize: mmap: %w", err)
	}

	return data, func() error { return syscall.Munmap(data) }, nil
}
