package service

import (
	"errors"
	"sync/atomic"
)

var (
	ErrManagedStateUnhealthy = errors.New("managed endpoint state is not healthy")
	managedStateHealthy      atomic.Bool
)

func init() {
	managedStateHealthy.Store(true)
}

func isManagedStateHealthy() bool {
	return managedStateHealthy.Load()
}

func setManagedStateHealthy(healthy bool) {
	managedStateHealthy.Store(healthy)
}
