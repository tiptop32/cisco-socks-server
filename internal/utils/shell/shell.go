// Package shell is the single entry point for spawning external commands, so
// every shell-out is bound to a context and cancellation propagates.
package shell

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Run executes name with args and returns its combined output. The output is
// returned even on failure: callers parse it or attach it to the error.
func Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return string(out), nil
}
