package main

import (
	"testing"

	"github.com/merzzzl/cisco-socks-server/internal/service"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := func() Config {
		return Config{
			CiscoUser:     "u",
			CiscoPassword: "p",
			CiscoProfile:  "vpn",
			DNSServers:    []string{"10.0.0.1"},
			LANClients:    []service.LANClient{{IP: "192.168.0.42", MAC: "72:ce:39:12:45:0f"}},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"missing user", func(c *Config) { c.CiscoUser = "" }, true},
		{"no dns servers", func(c *Config) { c.DNSServers = nil }, true},
		{"dns server with port", func(c *Config) { c.DNSServers = []string{"10.0.0.1:53"} }, true},
		{"dns hostname", func(c *Config) { c.DNSServers = []string{"dns.corp"} }, true},
		{"ipv6 lan client", func(c *Config) { c.LANClients[0].IP = "fe80::1" }, true},
		{"bad mac", func(c *Config) { c.LANClients[0].MAC = "zz" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid()
			tt.mutate(&cfg)

			if err := cfg.validate(); (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
