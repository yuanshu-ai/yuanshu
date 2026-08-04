//go:build darwin && cgo

package platform

/*
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework AppKit -framework Foundation
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

static int yuanshu_pick_directory(char **path, char **name) {
	__block int result = 0;
	__block char *selectedPath = NULL;
	__block char *selectedName = NULL;
	void (^showPanel)(void) = ^{
		@autoreleasepool {
			NSOpenPanel *panel = [NSOpenPanel openPanel];
			panel.canChooseFiles = NO;
			panel.canChooseDirectories = YES;
			panel.allowsMultipleSelection = NO;
			panel.canCreateDirectories = NO;
			panel.resolvesAliases = NO;
			panel.prompt = @"Choose Workspace";
			if ([panel runModal] == NSModalResponseOK && panel.URL.isFileURL) {
				const char *rawPath = panel.URL.path.fileSystemRepresentation;
				const char *rawName = panel.URL.lastPathComponent.UTF8String;
				if (rawPath != NULL && rawName != NULL) {
					selectedPath = strdup(rawPath);
					selectedName = strdup(rawName);
					result = selectedPath != NULL && selectedName != NULL ? 1 : -1;
				}
			}
		}
	};
	if ([NSThread isMainThread]) showPanel(); else dispatch_sync(dispatch_get_main_queue(), showPanel);
	*path = selectedPath;
	*name = selectedName;
	return result;
}
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

type darwinDirectoryPicker struct{}

func newDarwinDirectoryPicker() DirectoryPicker { return darwinDirectoryPicker{} }

func (darwinDirectoryPicker) Available() bool { return true }

func (darwinDirectoryPicker) PickDirectory(ctx context.Context) (DirectorySelection, error) {
	if ctx == nil || ctx.Err() != nil {
		return DirectorySelection{}, context.Canceled
	}
	var path, name *C.char
	result := int(C.yuanshu_pick_directory(&path, &name))
	if path != nil {
		defer C.free(unsafe.Pointer(path))
	}
	if name != nil {
		defer C.free(unsafe.Pointer(name))
	}
	if err := ctx.Err(); err != nil {
		return DirectorySelection{}, err
	}
	switch result {
	case 0:
		return DirectorySelection{}, context.Canceled
	case 1:
		return DirectorySelection{Path: C.GoString(path), DisplayName: C.GoString(name)}, nil
	default:
		return DirectorySelection{}, errors.New("macOS directory picker failed")
	}
}
