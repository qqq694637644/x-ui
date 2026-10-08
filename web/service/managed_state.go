package service

import (
	"errors"
	"sync/atomic"
)

var (
	ErrManagedStateUnhealthy = errors.New("managed endpoint state is not healthy")
	managedDataHealthy       atomic.Bool
	managedCaddyHealthy      atomic.Bool
	managedXrayHealthy       atomic.Bool
)

func init() {
	managedDataHealthy.Store(false)
	managedCaddyHealthy.Store(false)
	managedXrayHealthy.Store(false)
}

func isManagedStateHealthy() bool {
	return managedDataHealthy.Load() && managedCaddyHealthy.Load() && managedXrayHealthy.Load()
}

func setManagedStateHealthy(healthy bool) {
	managedDataHealthy.Store(healthy)
	managedCaddyHealthy.Store(healthy)
	managedXrayHealthy.Store(healthy)
}

func setManagedDataHealthy(healthy bool) {
	managedDataHealthy.Store(healthy)
}

func setManagedCaddyHealthy(healthy bool) {
	managedCaddyHealthy.Store(healthy)
}

func setManagedXrayHealthy(healthy bool) {
	managedXrayHealthy.Store(healthy)
}
