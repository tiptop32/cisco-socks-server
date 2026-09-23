package route

import "testing"

func TestParseDefaultNonTunnel(t *testing.T) {
	t.Parallel()

	const netstat = `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            link#22            UCSIg               utun4
default            192.168.0.1        UGScg                 en0
127                127.0.0.1          UCS                   lo0
`

	if got := parseDefaultNonTunnel(netstat); got != "en0" {
		t.Errorf("parseDefaultNonTunnel() = %q, want en0", got)
	}

	if got := parseDefaultNonTunnel("default link#22 UCSIg utun4\n"); got != "" {
		t.Errorf("tunnel-only default must yield empty, got %q", got)
	}
}

func TestArpEntryMatches(t *testing.T) {
	t.Parallel()

	const out = "? (192.168.0.42) at 72:ce:39:12:45:f on en0 permanent [ethernet]\n"

	tests := []struct {
		name       string
		mac, iface string
		want       bool
	}{
		{"leading zero normalized", "72:CE:39:12:45:0F", "en0", true},
		{"wrong interface", "72:ce:39:12:45:0f", "en1", false},
		{"wrong mac", "72:ce:39:12:45:10", "en0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := arpEntryMatches(out, tt.mac, tt.iface); got != tt.want {
				t.Errorf("arpEntryMatches() = %v, want %v", got, tt.want)
			}
		})
	}

	if arpEntryMatches("192.168.0.42 (192.168.0.42) -- no entry\n", "72:ce:39:12:45:0f", "en0") {
		t.Error("missing entry must not match")
	}
}
