package topology

import (
	"strings"
	"testing"
)

// Vectors produced by apps/hub/src/engine/ids.ts (node -e with dist/engine/ids.js).
// The hub resolves agent data by these names, so both sides must agree exactly.
var idVectors = []struct {
	key, slug, up, down, ss2053, ss443 string
}{
	{"link:e3:traffic-fallback", "link-e3-traffic-fallback", "l_e3_traffic_fallback_up", "l_e3_traffic_fallback_down", "link-e3-traffic-fallback-2053", "link-e3-traffic-fallback-443"},
	{"link:e3:traffic", "link-e3-traffic", "l_e3_traffic_up", "l_e3_traffic_down", "link-e3-traffic-2053", "link-e3-traffic-443"},
	{"link:e1:conns", "link-e1-conns", "l_e1_conns_up", "l_e1_conns_down", "link-e1-conns-2053", "link-e1-conns-443"},
	{"node:relay-1:conns", "node-relay-1-conns", "n_relay_1_conns_up", "n_relay_1_conns_down", "node-relay-1-conns-2053", "node-relay-1-conns-443"},
	{"node:iran-mullvad:conns", "node-iran-mullvad-conns", "n_iran_mullvad_conns_up", "n_iran_mullvad_conns_down", "node-iran-mullvad-conns-2053", "node-iran-mullvad-conns-443"},
	{"route:subscription:probe", "route-subscription-probe", "r_subscription_probe_up", "r_subscription_probe_down", "route-subscription-probe-2053", "route-subscription-probe-443"},
	{"node:panel:watchdog", "node-panel-watchdog", "n_panel_watchdog_up", "n_panel_watchdog_down", "node-panel-watchdog-2053", "node-panel-watchdog-443"},
	{"link:c3:http-probe", "link-c3-http-probe", "l_c3_http_probe_up", "l_c3_http_probe_down", "link-c3-http-probe-2053", "link-c3-http-probe-443"},
	{"Link:E9:Weird__Key--", "link-e9-weird-key", "L_e9_weird_key__up", "L_e9_weird_key__down", "link-e9-weird-key-2053", "link-e9-weird-key-443"},
	{"node:a.b_c:x/y z", "node-a-b-c-x-y-z", "n_a_b_c_x_y_z_up", "n_a_b_c_x_y_z_down", "node-a-b-c-x-y-z-2053", "node-a-b-c-x-y-z-443"},
	{"node:" + strings.Repeat("x", 80) + ":check-id",
		"node-" + strings.Repeat("x", 58),
		"n_" + strings.Repeat("x", 58) + "_up",
		"n_" + strings.Repeat("x", 56) + "_down",
		// SS ids keep the port when truncated, so ports never collide.
		"node-" + strings.Repeat("x", 53) + "-2053",
		"node-" + strings.Repeat("x", 54) + "-443"},
	{"--Leading:trail--", "leading-trail", "-_trail__up", "-_trail__down", "leading-trail-2053", "leading-trail-443"},
	{"route:main:probe", "route-main-probe", "r_main_probe_up", "r_main_probe_down", "route-main-probe-2053", "route-main-probe-443"},
	{"node:x:\u0130Ka", "node-x-i-ka", "n_x_i_ka_up", "n_x_i_ka_down", "node-x-i-ka-2053", "node-x-i-ka-443"},
}

func TestIDsMatchHub(t *testing.T) {
	for _, v := range idVectors {
		if got := Slug(v.key); got != v.slug {
			t.Errorf("Slug(%q) = %q, want %q", v.key, got, v.slug)
		}
		if got := HTTPID(v.key); got != v.slug {
			t.Errorf("HTTPID(%q) = %q, want %q", v.key, got, v.slug)
		}
		if got := LogID(v.key); got != v.slug {
			t.Errorf("LogID(%q) = %q, want %q", v.key, got, v.slug)
		}
		if got := NftName(v.key, "up"); got != v.up {
			t.Errorf("NftName(%q, up) = %q, want %q", v.key, got, v.up)
		}
		if got := NftName(v.key, "down"); got != v.down {
			t.Errorf("NftName(%q, down) = %q, want %q", v.key, got, v.down)
		}
		if got := SSID(v.key, 2053); got != v.ss2053 {
			t.Errorf("SSID(%q, 2053) = %q, want %q", v.key, got, v.ss2053)
		}
		if got := SSID(v.key, 443); got != v.ss443 {
			t.Errorf("SSID(%q, 443) = %q, want %q", v.key, got, v.ss443)
		}
	}
}

func TestNftNamesAreValidCounterNames(t *testing.T) {
	for _, v := range idVectors {
		for _, dir := range []string{"up", "down"} {
			if n := NftName(v.key, dir); len(n) > 63 {
				t.Errorf("NftName(%q) is %d chars, nft allows 63", v.key, len(n))
			}
		}
	}
}
