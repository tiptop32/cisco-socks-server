package cisco

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/merzzzl/cisco-socks-server/internal/utils/shell"
)

const ciscoPath = "/opt/cisco/secureclient/bin/vpn"

const (
	StateConnected    = "Connected"
	StateDisconnected = "Disconnected"
	StateReconnecting = "Reconnecting"
	StateUnknown      = "Unknown"
)

var (
	ErrNotConnected = errors.New("vpn connection not established")
	ErrAcquired     = errors.New("connect capability is unavailable, another Cisco application acquired it")
)

func Connect(ctx context.Context, profile, user, password string) error {
	// not shell.Run: credentials go through stdin
	cmd := exec.CommandContext(ctx, ciscoPath, "-s", "connect", profile)
	cmd.Stdin = strings.NewReader(user + "\n" + password + "\ny\n")

	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		return fmt.Errorf("vpn connection error: %w: %s", err, strings.TrimSpace(output))
	}

	if hasAcquiredError(output) {
		return ErrAcquired
	}

	// "-s connect" prints an unordered event stream: a successful connect can
	// still end with a stale ">> state: Disconnected" line, so ask the agent
	// for its actual state instead of trusting the stream tail.
	state, err := Status(ctx)
	if err != nil {
		return err
	}

	if state != StateConnected {
		return fmt.Errorf("%w (state %s): %s", ErrNotConnected, state, output)
	}

	return nil
}

func Status(ctx context.Context) (string, error) {
	out, err := shell.Run(ctx, ciscoPath, "-s", "state")
	if err != nil {
		return StateUnknown, fmt.Errorf("vpn state check error: %w", err)
	}

	return parseState(out), nil
}

func Disconnect(ctx context.Context) error {
	if _, err := shell.Run(ctx, ciscoPath, "-s", "disconnect"); err != nil {
		return fmt.Errorf("vpn disconnection error: %w", err)
	}

	return nil
}

func DisablePF(ctx context.Context) error {
	_, err := shell.Run(ctx, "pfctl", "-d")

	return err
}

func parseState(output string) string {
	var last string

	for line := range strings.SplitSeq(output, "\n") {
		if state, ok := strings.CutPrefix(strings.TrimSpace(line), ">> state: "); ok {
			last, _, _ = strings.Cut(state, " ")
		}
	}

	switch last {
	case "Подключено", "Connected":
		return StateConnected
	case "Отключено", "Disconnected":
		return StateDisconnected
	case "Reconnecting":
		return StateReconnecting
	default:
		return StateUnknown
	}
}

func hasAcquiredError(output string) bool {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, ">> error:") && strings.Contains(line, "Connect capability is unavailable") {
			return true
		}
	}

	return false
}
