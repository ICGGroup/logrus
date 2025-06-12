//go:build (linux || aix || zos) && !js && !wasi
// +build linux aix zos
// +build !js
// +build !wasi

package logrus

import "golang.org/x/sys/unix"

const ioctlReadTermios3 = unix.TCGETS

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlReadTermios3)
	return err == nil
}
