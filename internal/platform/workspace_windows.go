//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type windowsWorkspaceInspector struct{}

var _ WorkspaceInspector = (*windowsWorkspaceInspector)(nil)

func newWindowsWorkspaceInspector() WorkspaceInspector {
	return &windowsWorkspaceInspector{}
}

func (*windowsWorkspaceInspector) Available() bool { return true }

func (*windowsWorkspaceInspector) Inspect(ctx context.Context, path string) (WorkspaceFacts, error) {
	if ctx == nil {
		return WorkspaceFacts{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceFacts{}, err
	}
	clean, root, err := validateWindowsWorkspacePath(path)
	if err != nil {
		return WorkspaceFacts{}, err
	}
	crossesReparse, err := windowsPathCrossesReparse(ctx, clean, root)
	if err != nil {
		return WorkspaceFacts{}, err
	}

	pathPtr, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return WorkspaceFacts{}, ErrInvalidArgument
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return WorkspaceFacts{}, classifyWorkspaceWindowsError(err)
	}
	defer windows.CloseHandle(handle)

	canonical, err := finalWindowsPath(handle)
	if err != nil {
		return WorkspaceFacts{}, err
	}
	canonicalRoot, err := localWindowsRoot(canonical)
	if err != nil {
		return WorkspaceFacts{}, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return WorkspaceFacts{}, errors.New("platform workspace metadata is unavailable")
	}

	return WorkspaceFacts{
		CanonicalPath:          canonical,
		FilesystemRoot:         canonicalRoot,
		FileIdentity:           fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow),
		IsDirectory:            info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
		IsFilesystemRoot:       equalWindowsPath(canonical, canonicalRoot),
		IsHome:                 isWindowsHome(canonical),
		IsSystem:               isWindowsSystemPath(canonical),
		CrossesLinkBoundary:    crossesReparse,
		CrossesReparseBoundary: crossesReparse,
	}, nil
}

func validateWindowsWorkspacePath(path string) (clean string, root string, err error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", "", ErrInvalidArgument
	}
	clean = filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if len(volume) != 2 || volume[1] != ':' || !isASCIILetter(volume[0]) {
		return "", "", ErrInvalidArgument
	}
	root = strings.ToUpper(volume[:1]) + ":\\"
	rootPtr, pointerErr := windows.UTF16PtrFromString(root)
	if pointerErr != nil {
		return "", "", ErrInvalidArgument
	}
	switch windows.GetDriveType(rootPtr) {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE:
	default:
		return "", "", ErrInvalidArgument
	}
	return clean, root, nil
}

func windowsPathCrossesReparse(ctx context.Context, path, root string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, ErrInvalidArgument
	}
	current := root
	if relative == "." {
		return false, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		current = filepath.Join(current, component)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return false, ErrInvalidArgument
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil {
			return false, classifyWorkspaceWindowsError(err)
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return true, nil
		}
	}
	return false, nil
}

func finalWindowsPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("platform workspace canonical path is unavailable")
	}
	value := windows.UTF16ToString(buffer[:length])
	switch {
	case strings.HasPrefix(value, `\\?\UNC\`):
		return "", ErrInvalidArgument
	case strings.HasPrefix(value, `\\?\`):
		value = value[4:]
	}
	return filepath.Clean(value), nil
}

func localWindowsRoot(path string) (string, error) {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || !isASCIILetter(volume[0]) {
		return "", ErrInvalidArgument
	}
	return strings.ToUpper(volume[:1]) + ":\\", nil
}

func isWindowsHome(path string) bool {
	home, err := windows.KnownFolderPath(windows.FOLDERID_Profile, windows.KF_FLAG_DEFAULT)
	return err == nil && equalWindowsPath(path, home)
}

func isWindowsSystemPath(path string) bool {
	knownFolders := []*windows.KNOWNFOLDERID{
		windows.FOLDERID_Windows,
		windows.FOLDERID_ProgramFiles,
		windows.FOLDERID_ProgramFilesX86,
		windows.FOLDERID_ProgramData,
	}
	for _, folder := range knownFolders {
		root, err := windows.KnownFolderPath(folder, windows.KF_FLAG_DEFAULT)
		if err == nil && windowsPathWithin(path, root) {
			return true
		}
	}
	return false
}

func windowsPathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func equalWindowsPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func classifyWorkspaceWindowsError(err error) error {
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return ErrNotFound
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return errors.New("platform workspace access is unavailable")
	}
	return errors.New("platform workspace inspection failed")
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
