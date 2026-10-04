//go:build !linux

package app

import (
	"context"
	"errors"
)

func linuxUserInstallEligible(string) bool { return false }

func queueLinuxUpdateRestart(context.Context, string, string, *updateCoordinator, func()) error {
	return errors.New("Linux updater is unavailable on this platform")
}
