//go:build !linux

package sysmetrics

import "errors"

// statfs: the module only runs on Linux, which has /proc.
func statfs(string) (total, free uint64, err error) { return 0, 0, errors.ErrUnsupported }
