//go:build !profile

package backend

func startProfiler() ([]string, func()) {
	return nil, func() {}
}
