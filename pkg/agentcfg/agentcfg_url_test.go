package agentcfg

import "testing"

func TestHubURLRejectsTricks(t *testing.T) {
	good := []string{"https://map.imdev.ir", "http://127.0.0.1:4000", "http://localhost:4000", "http://[::1]:4000"}
	bad := []string{"http://127.0.0.1@evil.example", "http://127.0.0.1.evil.example", "http://map.imdev.ir", "https://user:pw@map.imdev.ir", "ftp://x", ""}
	for _, u := range good {
		if !hubURLOK(u) {
			t.Errorf("%s should be accepted", u)
		}
	}
	for _, u := range bad {
		if hubURLOK(u) {
			t.Errorf("%s should be rejected", u)
		}
	}
}
