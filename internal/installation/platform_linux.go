package installation

import (
	"fmt"
	"os"
	"syscall"
)

func lockDirectory(dir string) (func(), error) {
	f, err := os.OpenFile(dir+"/.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another installation command is running: %w", err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func socketGroup(path string) (uint32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return 0, fmt.Errorf("%s is not a Docker socket", path)
	}
	return info.Sys().(*syscall.Stat_t).Gid, nil
}
