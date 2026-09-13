package components

import (
	"runtime"
	"slices"
)

// arch is this odio's dpkg architecture: each .deb is built for one, so the
// binary's GOARCH names it (GOARM=6 builds are armhf). A var, for tests.
var arch = debArch(runtime.GOARCH)

func debArch(goarch string) string {
	if goarch == "arm" {
		return "armhf"
	}
	return goarch
}

// supported reports whether the role installs on this odio's architecture.
func (info RoleInfo) supported() bool {
	return len(info.Archs) == 0 || slices.Contains(info.Archs, arch)
}
