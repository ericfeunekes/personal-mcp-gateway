package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type candidateSnapshotter func(string, string) (string, func(), error)

type candidateSnapshotHooks struct {
	afterSourceOpen func()
}

func createPrivateCandidateSnapshot(candidatePath, expectedSHA256 string) (string, func(), error) {
	return createPrivateCandidateSnapshotWithHooks(candidatePath, expectedSHA256, candidateSnapshotHooks{})
}

func createPrivateCandidateSnapshotWithHooks(candidatePath, expectedSHA256 string, hooks candidateSnapshotHooks) (string, func(), error) {
	if candidatePath == "" || !validDigest(expectedSHA256) {
		return "", nil, errors.New("candidate snapshot failed")
	}
	directory, err := os.MkdirTemp("", "personal-mcp-gateway-smoke-candidate-")
	if err != nil {
		return "", nil, errors.New("candidate snapshot failed")
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	fail := func() (string, func(), error) {
		cleanup()
		return "", nil, errors.New("candidate snapshot failed")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fail()
	}

	sourceFD, err := unix.Open(candidatePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fail()
	}
	source := os.NewFile(uintptr(sourceFD), "candidate-source")
	pathInfo, pathErr := os.Lstat(candidatePath)
	openInfo, statErr := source.Stat()
	if pathErr != nil || statErr != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		pathInfo.Mode().Perm()&0o111 == 0 || !openInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openInfo) ||
		openInfo.Size() < 1 || openInfo.Size() > maxCandidateBytes {
		_ = source.Close()
		return fail()
	}
	if hooks.afterSourceOpen != nil {
		hooks.afterSourceOpen()
	}

	snapshotPath := filepath.Join(directory, "candidate")
	destination, err := os.OpenFile(snapshotPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		_ = source.Close()
		return fail()
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(source, maxCandidateBytes+1))
	sourceFinal, sourceStatErr := source.Stat()
	sourceCloseErr := source.Close()
	destinationSyncErr := destination.Sync()
	destinationCloseErr := destination.Close()
	if copyErr != nil || sourceStatErr != nil || sourceCloseErr != nil || destinationSyncErr != nil || destinationCloseErr != nil ||
		written != openInfo.Size() || written > maxCandidateBytes || sourceFinal.Size() != openInfo.Size() ||
		hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail()
	}
	if err := os.Chmod(snapshotPath, 0o700); err != nil {
		return fail()
	}
	snapshotInfo, err := os.Lstat(snapshotPath)
	if err != nil || !snapshotInfo.Mode().IsRegular() || snapshotInfo.Mode()&os.ModeSymlink != 0 || snapshotInfo.Mode().Perm() != 0o700 {
		return fail()
	}
	snapshotHash, err := hashRegularBounded(snapshotPath, maxCandidateBytes)
	if err != nil || snapshotHash != expectedSHA256 {
		return fail()
	}
	return snapshotPath, cleanup, nil
}
