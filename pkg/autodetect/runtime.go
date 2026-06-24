package autodetect

import (
	"os"
)

// RuntimeType identifies the underlying container runtime.
type RuntimeType string

const (
	RuntimeDocker     RuntimeType = "docker"
	RuntimeContainerd RuntimeType = "containerd"
	RuntimeCRIO       RuntimeType = "cri-o"
	RuntimeCgroups    RuntimeType = "cgroups"
	RuntimeUnknown    RuntimeType = "unknown"
)

// DetectRuntime returns the active runtime by checking standard socket/fs paths.
// Order of precedence: Docker -> Containerd -> CRI-O -> cgroups.
func DetectRuntime() RuntimeType {
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return RuntimeDocker
	}
	if _, err := os.Stat("/run/containerd/containerd.sock"); err == nil {
		return RuntimeContainerd
	}
	if _, err := os.Stat("/run/crio/crio.sock"); err == nil {
		return RuntimeCRIO
	}
	if _, err := os.Stat("/sys/fs/cgroup"); err == nil {
		return RuntimeCgroups
	}
	return RuntimeUnknown
}
