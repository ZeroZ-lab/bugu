//go:build !darwin && !linux

package store

import "errors"

func lock(path string) (func(), error) {
	return nil, errors.New("store: unsupported operating system")
}
