//go:build darwin || linux

package store

import (
	"errors"
	"os"
	"syscall"
)

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	// 进程退出时内核自动释放,所以崩溃不会留下死锁。
	return func() { f.Close() }, nil
}
