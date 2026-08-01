//go:build darwin

package platform

func Current() Platform {
	return newUnavailablePlatform(FamilyDarwin)
}
