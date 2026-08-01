//go:build linux

package platform

func Current() Platform {
	return newUnavailablePlatform(FamilyLinux)
}
