// Package avahi provides mechanisms to signal avahi-daemon to reload its configuration.
package avahi

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	// systemd D-Bus — used to run `systemctl reload <service>`
	systemdBusName    = "org.freedesktop.systemd1"
	systemdObjectPath = "/org/freedesktop/systemd1"
	systemdInterface  = "org.freedesktop.systemd1.Manager"
	systemdReloadUnit = systemdInterface + ".ReloadUnit"

	// DefaultServiceName is the systemd unit name for avahi-daemon on most distros.
	// Override with --avahi-service if your distro uses a different name.
	DefaultServiceName = "avahi-daemon.service"
)

// Reloader abstracts the avahi reload mechanism.
type Reloader interface {
	Reload() error
}

// SystemdReloader signals avahi-daemon via systemd on the system D-Bus.
// This is equivalent to `systemctl reload <ServiceName>` and sends SIGHUP
// to avahi-daemon, causing it to re-read its hosts file.
type SystemdReloader struct {
	ServiceName string // e.g. "avahi-daemon.service" or "avahi.service"
}

func (r *SystemdReloader) Reload() error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect to system bus: %w", err)
	}
	defer conn.Close()

	obj := conn.Object(systemdBusName, dbus.ObjectPath(systemdObjectPath))

	var jobPath dbus.ObjectPath
	call := obj.Call(systemdReloadUnit, 0, r.ServiceName, "replace")
	if call.Err != nil {
		return fmt.Errorf("systemd ReloadUnit(%s): %w", r.ServiceName, call.Err)
	}
	if err := call.Store(&jobPath); err != nil {
		return fmt.Errorf("store job path: %w", err)
	}
	return nil
}

// NewDefaultReloader returns a SystemdReloader for the given service name.
func NewDefaultReloader(serviceName string) Reloader {
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	return &SystemdReloader{ServiceName: serviceName}
}
