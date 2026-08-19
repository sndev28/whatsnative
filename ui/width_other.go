//go:build !unix

package ui

import (
	"errors"
	"time"
)

// readByteBefore has no portable implementation with a timeout, so terminals
// on these platforms simply go unmeasured and the estimates are used instead.
func readByteBefore(fd int, deadline time.Time) (byte, error) {
	return 0, errors.New("cursor reports unsupported on this platform")
}
