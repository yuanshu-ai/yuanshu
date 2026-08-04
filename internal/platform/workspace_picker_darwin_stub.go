//go:build darwin && !cgo

package platform

import "context"

type darwinDirectoryPicker struct{}

func newDarwinDirectoryPicker() DirectoryPicker { return darwinDirectoryPicker{} }
func (darwinDirectoryPicker) Available() bool   { return false }
func (darwinDirectoryPicker) PickDirectory(context.Context) (DirectorySelection, error) {
	return DirectorySelection{}, ErrUnavailable
}
