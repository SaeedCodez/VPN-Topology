package promtext

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseFixture(t *testing.T) {
	f, err := os.Open("testdata/metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allow := map[string]bool{
		"remnawave_users_online": true, "remnawave_users_total": true,
		"remnawave_node_traffic_bytes_total": true, "remnawave_sync_lag_seconds": true,
		"remnawave_last_error_ratio": true, "http_request_duration_seconds_bucket": true,
	}
	got, dropped, err := Parse(f, allow, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []Sample{
		{Name: "remnawave_users_online", Value: 137},
		{Name: "remnawave_users_total", Labels: map[string]string{"status": "active"}, Value: 1200},
		{Name: "remnawave_users_total", Labels: map[string]string{"status": "disabled"}, Value: 45},
		{Name: "remnawave_users_total", Labels: map[string]string{"status": "limited", "note": "a \"quoted\" \\ value\nnext"}, Value: 3},
		{Name: "remnawave_node_traffic_bytes_total", Labels: map[string]string{"node": "relay-1", "direction": "rx"}, Value: 1.234e12},
		{Name: "remnawave_node_traffic_bytes_total", Labels: map[string]string{"node": "relay-1", "direction": "tx"}, Value: 9.87e11},
		{Name: "http_request_duration_seconds_bucket", Labels: map[string]string{"le": "0.1"}, Value: 10},
		{Name: "http_request_duration_seconds_bucket", Labels: map[string]string{"le": "+Inf"}, Value: 12},
		// NaN and +Inf values are skipped: JSON cannot encode them.
	}
	if dropped != 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("dropped=%d\ngot  %+v\nwant %+v", dropped, got, want)
	}
}

func TestParseAllowListAndMax(t *testing.T) {
	in := "a 1\nb 2\na{x=\"1\"} 3\na{x=\"2\"} 4\n"
	got, dropped, err := Parse(strings.NewReader(in), map[string]bool{"a": true}, 2)
	if err != nil || len(got) != 2 || dropped != 1 {
		t.Errorf("got %v dropped %d err %v", got, dropped, err)
	}
	got, _, _ = Parse(strings.NewReader(in), nil, 10)
	if len(got) != 0 {
		t.Errorf("empty allow-list kept %v", got)
	}
}

func TestParseLineErrors(t *testing.T) {
	for _, l := range []string{`{a="b"} 1`, `m{a="b} 1`, `m{a=b} 1`, `m`, `m 1 2 3`, `m abc`, `m{="x"} 1`} {
		if _, err := ParseLine(l); err == nil {
			t.Errorf("ParseLine(%q) accepted", l)
		}
	}
	if _, _, err := Parse(strings.NewReader("m{a=\"b} 1\n"), map[string]bool{"m": true}, 10); err == nil {
		t.Error("Parse must report malformed allow-listed lines")
	}
}
