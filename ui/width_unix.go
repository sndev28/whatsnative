//go:build unix

package ui

import (
	"errors"
	"io"
	"time"

	"golang.org/x/sys/unix"
)

var errProbeTimeout = errors.New("terminal did not answer")

// readByteBefore waits for one byte from fd, giving up at the deadline.
//
// It polls the descriptor rather than using os.File deadlines, which Go does
// not support on a terminal. Giving up matters: a blocked read would still be
// holding stdin when Bubble Tea starts, and would swallow the user's keys.
func readByteBefore(fd int, deadline time.Time) (byte, error) {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, errProbeTimeout
		}

		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, int(remaining.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if ready == 0 {
			return 0, errProbeTimeout
		}

		var buf [1]byte
		n, err := unix.Read(fd, buf[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return buf[0], nil
	}
}
