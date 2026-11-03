//go:build !linux && !darwin

package ctl

import "errors"

// userConfigDir is unused here: there is no sudo to read a login through.
const userConfigDir = ""

func readOwnedBy(string, string) ([]byte, error) {
	return nil, errors.New("reading another user's login is not supported on this system")
}
