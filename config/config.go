// Package config carries the build-time identity of odioctl.
package config

const AppName = "odioctl"

// AppVersion is set at build time via
// -ldflags "-X github.com/b0bbywan/odioctl/config.AppVersion=x.y.z".
var AppVersion = "dev"
