#!/bin/sh
set -eu
chart=charts/avahi-controller
default=$(helm template avahi-controller "$chart")
if printf '%s' "$default" | grep -Eq 'system_bus_socket|DBUS_SYSTEM_BUS_ADDRESS'; then
  echo 'default chart exposes the system bus' >&2
  exit 1
fi
printf '%s' "$default" | grep -Eq -e '--reload=false'
enabled=$(helm template avahi-controller "$chart" --set reload.enabled=true)
printf '%s' "$enabled" | grep -Eq 'system_bus_socket'
printf '%s' "$enabled" | grep -Eq 'DBUS_SYSTEM_BUS_ADDRESS'
printf '%s' "$enabled" | grep -Eq -e '--reload=true'
helm lint "$chart"
