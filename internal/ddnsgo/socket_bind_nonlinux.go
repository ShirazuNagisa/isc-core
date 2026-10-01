//go:build !linux

package ddnsgo

import "net"

func setLinuxBindToDevice(boundDialer *net.Dialer, ifaceName string) {
}
