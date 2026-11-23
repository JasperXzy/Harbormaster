package netprobe

import "runtime"

// TraceMethods describes implementation support, not runtime socket permission.
func TraceMethods() []string {
	methods := []string{"icmp"}
	if runtime.GOOS == "linux" {
		methods = append(methods, "tcp")
	}
	return methods
}
