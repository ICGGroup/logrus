//go:build (darwin || dragonfly || freebsd || netbsd || openbsd || hurd) && !js
// +build darwin dragonfly freebsd netbsd openbsd hurd
// +build !js

package logrus

import "golang.org/x/sys/unix"

const ioctlReadTermios2 = unix.TIOCGETA

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlReadTermios2)
	return err == nil
}
