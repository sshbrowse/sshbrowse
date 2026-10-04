//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const (
	linuxUpdatePreflightTimeout = 15 * time.Second
	maxLinuxUpdateBytes         = 512 * 1024 * 1024
)

func linuxUserInstallEligible(executablePath string) bool {
	_, err := linuxUserInstallPath(executablePath)
	return err == nil
}

func linuxUserInstallPath(executablePath string) (string, error) {
	if os.Geteuid() == 0 {
		return "", errors.New("in-app updates are disabled for root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return linuxUserInstallTarget(home, executablePath, uint32(os.Geteuid()))
}

func linuxUserInstallTarget(home, executablePath string, uid uint32) (string, error) {
	if !filepath.IsAbs(home) || executablePath == "" {
		return "", errors.New("installation paths must be absolute")
	}
	home, err := filepath.EvalSymlinks(filepath.Clean(home))
	if err != nil {
		return "", err
	}
	target := filepath.Join(home, ".local", "bin", "sshbrowse")
	for index, directory := range []string{home, filepath.Join(home, ".local"), filepath.Dir(target)} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !linuxOwnedBy(info, uid) || info.Mode().Perm()&0o022 != 0 {
			return "", fmt.Errorf("unsafe update directory %s", directory)
		}
		if index == 2 && info.Mode().Perm()&0o200 == 0 {
			return "", errors.New("update directory is not user-writable")
		}
	}
	executable, err := filepath.Abs(executablePath)
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || filepath.Clean(executable) != target {
		return "", errors.New("executable is not ~/.local/bin/sshbrowse")
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !linuxOwnedBy(info, uid) || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("executable is not a safe user-owned file")
	}
	return target, nil
}

func linuxOwnedBy(info os.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid
}

type preparedLinuxUpdate struct {
	target, candidate, recovery, directory string
}

func (prepared preparedLinuxUpdate) cleanup() { _ = os.RemoveAll(prepared.directory) }

func queueLinuxUpdateRestart(ctx context.Context, executablePath, downloadedPath string, coordinator *updateCoordinator, quit func()) error {
	if coordinator == nil || coordinator.linuxFinish == nil || quit == nil {
		return errors.New("Linux update restart is unavailable")
	}
	prepared, err := prepareLinuxUpdate(ctx, executablePath, downloadedPath)
	if err != nil {
		return err
	}
	finish := func() error {
		// DownloadedPath comes from Wails' private staging directory.
		defer func() {
			_ = os.Remove(downloadedPath)
			_ = os.Remove(filepath.Dir(downloadedPath))
		}()
		return finishLinuxUpdate(prepared)
	}
	select {
	case coordinator.linuxFinish <- finish:
		quit()
		return nil
	default:
		prepared.cleanup()
		return errors.New("another Linux update is pending")
	}
}

func prepareLinuxUpdate(ctx context.Context, executablePath, downloadedPath string) (preparedLinuxUpdate, error) {
	if err := ctx.Err(); err != nil {
		return preparedLinuxUpdate{}, err
	}
	target, err := linuxUserInstallPath(executablePath)
	if err != nil {
		return preparedLinuxUpdate{}, err
	}
	directory, err := os.MkdirTemp(filepath.Dir(target), ".sshbrowse-update-*")
	if err != nil {
		return preparedLinuxUpdate{}, err
	}
	prepared := preparedLinuxUpdate{
		target:    target,
		candidate: filepath.Join(directory, "candidate"),
		recovery:  filepath.Join(directory, "recovery"),
		directory: directory,
	}
	keep := false
	defer func() {
		if !keep {
			prepared.cleanup()
		}
	}()
	if err := copyLinuxUpdate(ctx, downloadedPath, prepared.candidate); err != nil {
		return preparedLinuxUpdate{}, fmt.Errorf("stage update: %w", err)
	}
	preflight, cancel := context.WithTimeout(ctx, linuxUpdatePreflightTimeout)
	defer cancel()
	command := exec.CommandContext(preflight, prepared.candidate, "--licenses")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		if preflight.Err() != nil {
			return preparedLinuxUpdate{}, fmt.Errorf("update preflight: %w", preflight.Err())
		}
		return preparedLinuxUpdate{}, fmt.Errorf("update preflight: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return preparedLinuxUpdate{}, err
	}
	if err := os.Link(target, prepared.recovery); err != nil {
		return preparedLinuxUpdate{}, fmt.Errorf("prepare recovery link: %w", err)
	}
	keep = true
	return prepared, nil
}

func copyLinuxUpdate(ctx context.Context, source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxLinuxUpdateBytes {
		return errors.New("update is not a regular file within the size limit")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	defer output.Close()
	count, err := io.Copy(output, io.LimitReader(input, maxLinuxUpdateBytes+1))
	if err != nil {
		return err
	}
	if count > maxLinuxUpdateBytes {
		return errors.New("update exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := output.Chmod(0o755); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	return output.Close()
}

func finishLinuxUpdate(prepared preparedLinuxUpdate) error {
	if err := os.Rename(prepared.candidate, prepared.target); err != nil {
		launchErr := launchLinuxExecutable(prepared.target)
		prepared.cleanup()
		if launchErr != nil {
			return fmt.Errorf("replace executable: %v; relaunch old executable: %w", err, launchErr)
		}
		return fmt.Errorf("replace executable: %w", err)
	}
	if err := launchLinuxExecutable(prepared.target); err != nil {
		if restoreErr := os.Rename(prepared.recovery, prepared.target); restoreErr != nil {
			return fmt.Errorf("launch update: %v; restore failed: %w; recovery retained in %s", err, restoreErr, prepared.directory)
		}
		relaunchErr := launchLinuxExecutable(prepared.target)
		prepared.cleanup()
		if relaunchErr != nil {
			return fmt.Errorf("launch update: %v; relaunch old executable: %w", err, relaunchErr)
		}
		return fmt.Errorf("launch update: %w (old executable relaunched)", err)
	}
	prepared.cleanup()
	return nil
}

func launchLinuxExecutable(path string) error {
	command := exec.Command(path)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
