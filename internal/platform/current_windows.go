//go:build windows

package platform

func Current() Platform {
	return newUnavailablePlatform(FamilyWindows)
}
