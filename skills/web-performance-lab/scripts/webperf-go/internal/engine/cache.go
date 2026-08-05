package engine

import (
	"errors"
	"os"
)

type managedHierarchy struct {
	root os.FileInfo
	dirs []os.FileInfo
}

func ensureCacheRoot(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ErrCache
	}
	info, err := os.Lstat(path)
	if err != nil || !trustedDirectory(info) {
		return ErrCache
	}
	return nil
}

func ensureManagedDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrCache
	}
	info, err := os.Lstat(path)
	if err != nil || !trustedDirectory(info) {
		return ErrCache
	}
	return nil
}

func inspectManagedHierarchy(root string, paths []string) (managedHierarchy, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || !trustedDirectory(rootInfo) {
		return managedHierarchy{}, ErrCache
	}
	snapshot := managedHierarchy{root: rootInfo, dirs: make([]os.FileInfo, 0, len(paths))}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !trustedDirectory(info) {
			return managedHierarchy{}, ErrCache
		}
		snapshot.dirs = append(snapshot.dirs, info)
	}
	return snapshot, nil
}

func sameManagedHierarchy(left, right managedHierarchy) bool {
	if !sameStableEntry(left.root, right.root) || len(left.dirs) != len(right.dirs) {
		return false
	}
	for index := range left.dirs {
		if !sameStableEntry(left.dirs[index], right.dirs[index]) {
			return false
		}
	}
	return true
}

func trustedDirectory(info os.FileInfo) bool {
	return trustedEntry(info) && info.IsDir()
}

func trustedRegularFile(info os.FileInfo) bool {
	return trustedEntry(info) && info.Mode().IsRegular()
}

func trustedEntry(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !safeEntryPermissions(info.Mode()) {
		return false
	}
	return effectiveUserOwns(info)
}

func sameStableEntry(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
