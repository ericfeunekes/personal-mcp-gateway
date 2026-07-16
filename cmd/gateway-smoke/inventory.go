package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"personal-mcp-gateway/internal/tools/obsidian"
)

const (
	inventoryBatchSize          = 128
	inventoryMaxDepth           = 256
	inventoryCardinalityCeiling = 1001
)

var errInventoryStopped = errors.New("vault aggregate inventory stopped")

type inventoryOptions struct {
	maxFiles uint64
	maxBytes uint64
	timeout  time.Duration
	hooks    inventoryHooks
}

type inventoryHooks struct {
	beforeOpenDirectory func(string)
}

type inventoryState struct {
	profile vaultAggregateProfile
	options inventoryOptions
}

func defaultInventoryOptions() inventoryOptions {
	return inventoryOptions{
		maxFiles: uint64(obsidian.MaxGrepMaxFiles),
		maxBytes: uint64(obsidian.MaxGrepMaxBytes),
		timeout:  2 * time.Second,
	}
}

func inspectVaultAggregate(ctx context.Context, root string) (vaultAggregateProfile, error) {
	return inspectVaultAggregateWithOptions(ctx, root, defaultInventoryOptions())
}

func inspectVaultAggregateWithOptions(ctx context.Context, root string, options inventoryOptions) (vaultAggregateProfile, error) {
	profile := vaultAggregateProfile{InventoryPolicy: markdownInventoryPolicy, StoppedBy: "scope"}
	if ctx == nil || root == "" || options.maxFiles == 0 || options.maxBytes == 0 || options.timeout <= 0 {
		return vaultAggregateProfile{}, errors.New("vault aggregate inventory failed")
	}
	rootDirectory, err := openInventoryRoot(root)
	if err != nil {
		return vaultAggregateProfile{}, errors.New("vault aggregate inventory failed")
	}
	defer rootDirectory.Close()

	inventoryCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	state := &inventoryState{profile: profile, options: options}
	err = walkInventoryDirectory(inventoryCtx, rootDirectory, 0, state)
	if err != nil && !errors.Is(err, errInventoryStopped) {
		return vaultAggregateProfile{}, errors.New("vault aggregate inventory failed")
	}
	state.profile.InventoryComplete = state.profile.StoppedBy == "scope"
	return state.profile, nil
}

func inspectRootCardinality(ctx context.Context, root string) (int, error) {
	if ctx == nil || root == "" {
		return 0, errors.New("current-vault cardinality measurement failed")
	}
	directory, err := openInventoryRoot(root)
	if err != nil {
		return 0, errors.New("current-vault cardinality measurement failed")
	}
	defer directory.Close()
	initial, err := directory.Stat()
	if err != nil {
		return 0, errors.New("current-vault cardinality measurement failed")
	}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, errors.New("current-vault cardinality measurement failed")
		}
		entries, readErr := directory.ReadDir(inventoryBatchSize)
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return 0, errors.New("current-vault cardinality measurement failed")
			}
			count++
			if count >= inventoryCardinalityCeiling {
				return inventoryCardinalityCeiling, nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, errors.New("current-vault cardinality measurement failed")
		}
	}
	final, err := directory.Stat()
	if err != nil || !os.SameFile(initial, final) || !initial.ModTime().Equal(final.ModTime()) {
		return 0, errors.New("current-vault cardinality measurement failed")
	}
	return count, nil
}

func openInventoryRoot(root string) (*os.File, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	var baseline unix.Stat_t
	if err := unix.Lstat(absolute, &baseline); err != nil || baseline.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, errors.New("invalid inventory root")
	}
	fd, err := unix.Open(absolute, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameInventoryNode(baseline, opened, false) {
		_ = unix.Close(fd)
		return nil, errors.New("invalid inventory root")
	}
	return os.NewFile(uintptr(fd), "vault-inventory-root"), nil
}

func walkInventoryDirectory(ctx context.Context, directory *os.File, depth int, state *inventoryState) error {
	if depth > inventoryMaxDepth {
		return errors.New("inventory depth exceeded")
	}
	initial, err := directory.Stat()
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return state.stop("timeout")
		}
		entries, readErr := directory.ReadDir(inventoryBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return state.stop("timeout")
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			var baseline unix.Stat_t
			if err := unix.Fstatat(int(directory.Fd()), name, &baseline, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if inventoryMutationError(err) {
					return state.stop("source_change")
				}
				return err
			}
			switch baseline.Mode & unix.S_IFMT {
			case unix.S_IFLNK:
				continue
			case unix.S_IFDIR:
				if state.options.hooks.beforeOpenDirectory != nil {
					state.options.hooks.beforeOpenDirectory(name)
				}
				childFD, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					if inventoryMutationError(err) {
						return state.stop("source_change")
					}
					return err
				}
				child := os.NewFile(uintptr(childFD), "vault-inventory-directory")
				var opened unix.Stat_t
				if err := unix.Fstat(childFD, &opened); err != nil || !sameInventoryNode(baseline, opened, false) {
					_ = child.Close()
					return state.stop("source_change")
				}
				walkErr := walkInventoryDirectory(ctx, child, depth+1, state)
				closeErr := child.Close()
				if walkErr != nil {
					return walkErr
				}
				if closeErr != nil {
					return closeErr
				}
				var final unix.Stat_t
				if err := unix.Fstatat(int(directory.Fd()), name, &final, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameInventoryNode(baseline, final, false) {
					return state.stop("source_change")
				}
			case unix.S_IFREG:
				if !strings.EqualFold(path.Ext(name), ".md") {
					continue
				}
				if baseline.Size < 0 {
					return errors.New("invalid inventory file size")
				}
				if state.profile.MarkdownFileCount >= state.options.maxFiles {
					return state.stop("file_limit")
				}
				size := uint64(baseline.Size)
				if size > state.options.maxBytes-state.profile.MarkdownByteCount {
					return state.stop("byte_limit")
				}
				var final unix.Stat_t
				if err := unix.Fstatat(int(directory.Fd()), name, &final, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameInventoryNode(baseline, final, true) {
					return state.stop("source_change")
				}
				state.profile.MarkdownFileCount++
				state.profile.MarkdownByteCount += size
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	final, err := directory.Stat()
	if err != nil || !os.SameFile(initial, final) || !initial.ModTime().Equal(final.ModTime()) {
		return state.stop("source_change")
	}
	return nil
}

func (state *inventoryState) stop(reason string) error {
	state.profile.StoppedBy = reason
	return errInventoryStopped
}

func sameInventoryNode(left, right unix.Stat_t, includeSize bool) bool {
	if left.Dev != right.Dev || left.Ino != right.Ino || left.Mode != right.Mode {
		return false
	}
	return !includeSize || left.Size == right.Size
}

func inventoryMutationError(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ESTALE)
}
