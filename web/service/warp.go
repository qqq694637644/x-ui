package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RestartWireProxy restarts only the locally installed systemd WireProxy unit.
// No shell, user-controlled unit name, or fallback to direct networking.
func RestartWireProxy() error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", "restart", "wireproxy.service").CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("restart wireproxy.service timed out: %w", ctx.Err())
		}
		return fmt.Errorf("restart wireproxy.service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
