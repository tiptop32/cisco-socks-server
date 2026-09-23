package cisco

import "testing"

func TestParseState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"connected", "  >> state: Connected\n", StateConnected},
		{"russian connected", ">> state: Подключено\n", StateConnected},
		{"russian disconnected", ">> state: Отключено\n", StateDisconnected},
		{"reconnecting", ">> state: Reconnecting\n", StateReconnecting},
		{"last line wins", ">> state: Disconnected\n>> state: Connected\n", StateConnected},
		{"trailing words ignored", ">> state: Connected (some detail)\n", StateConnected},
		{"no state lines", "VPN>\n>> notice: hello\n", StateUnknown},
		{"empty", "", StateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := parseState(tt.output); got != tt.want {
				t.Errorf("parseState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasAcquiredError(t *testing.T) {
	t.Parallel()

	if !hasAcquiredError("  >> error: Connect capability is unavailable because the VPN service is in use\n") {
		t.Error("expected acquired error to be detected")
	}

	if hasAcquiredError(">> notice: Connect capability is unavailable\n") {
		t.Error("non-error line must not match")
	}
}
