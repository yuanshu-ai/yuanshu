//go:build darwin

package main

import "runtime"

func prepareUIThread() {
	runtime.LockOSThread()
}
