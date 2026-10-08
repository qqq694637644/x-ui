package service

import (
	"errors"
	"sync/atomic"
)

var (
	ErrManagedStateUnhealthy = errors.New("托管状态未通过校验；请重启 x-ui 面板完成全量校验后重试")
	managedDataHealthy       atomic.Bool
	managedCaddyHealthy      atomic.Bool
	managedXrayHealthy       atomic.Bool
)

const managedPublicPort = 443

func init() {
	managedDataHealthy.Store(false)
	managedCaddyHealthy.Store(false)
	managedXrayHealthy.Store(false)
}

func isManagedStateHealthy() bool {
	return managedDataHealthy.Load() && managedCaddyHealthy.Load() && managedXrayHealthy.Load()
}

func isManagedDataHealthy() bool {
	return managedDataHealthy.Load()
}

func isManagedCaddyHealthy() bool {
	return managedCaddyHealthy.Load()
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
