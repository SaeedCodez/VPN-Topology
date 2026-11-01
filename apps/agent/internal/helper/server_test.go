package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

func testConfig() *agentcfg.Config {
	return &agentcfg.Config{
		Hub: "https://map.example.com", Node: "relay-1", TokenFile: "/etc/vpnmap-agent/token",
		Containers: []string{"backend-app-1", "remnawave", "gone"},
		Nft:        &agentcfg.NftConfig{Counters: []agentcfg.NftCounter{{Name: "relay_in_2053", Hook: "input", Proto: "tcp", Dport: 2053}}},
		WG:         []agentcfg.WGConfig{{Iface: "wg0"}, {Netns: "mullvad", Iface: "wg-mv"}},
		ChecksDir:  "/etc/vpnmap-agent/checks.d",
		Checks:     []agentcfg.LocalCheck{{ID: "xbridger-doctor"}, {ID: "relay-forwards", Timeout: agentcfg.Duration(28 * time.Second)}},
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestValidate(t *testing.T) {
	s := &Server{Cfg: testConfig()}
	ok := []Request{
		{Op: OpNftCounters},
		{Op: OpDockerPS},
		{Op: OpWGDump, Args: []string{"", "wg0"}},
		{Op: OpWGDump, Args: []string{"mullvad", "wg-mv"}},
		{Op: OpCheck, Args: []string{"xbridger-doctor"}},
	}
	for _, r := range ok {
		if err := s.Validate(r); err != nil {
			t.Errorf("Validate(%+v) = %v, want ok", r, err)
		}
	}
	bad := map[string]Request{
		"unknown op":              {Op: "exec", Args: []string{"rm -rf /"}},
		"empty op":                {Op: ""},
		"op case":                 {Op: "NFT-COUNTERS"},
		"nft with args":           {Op: OpNftCounters, Args: []string{"table", "ip", "filter"}},
		"docker with args":        {Op: OpDockerPS, Args: []string{"all"}},
		"wg one arg":              {Op: OpWGDump, Args: []string{"wg0"}},
		"wg not configured":       {Op: OpWGDump, Args: []string{"", "wg9"}},
		"wg wrong netns":          {Op: OpWGDump, Args: []string{"", "wg-mv"}},
		"wg netns not configured": {Op: OpWGDump, Args: []string{"other", "wg0"}},
		"wg traversal iface":      {Op: OpWGDump, Args: []string{"", "../wg0"}},
		"wg traversal netns":      {Op: OpWGDump, Args: []string{"../../proc/1/ns/net", "wg0"}},
		"wg option injection":     {Op: OpWGDump, Args: []string{"", "--help"}},
		"check no args":           {Op: OpCheck},
		"check traversal":         {Op: OpCheck, Args: []string{"../x"}},
		"check traversal deep":    {Op: OpCheck, Args: []string{"../../../usr/bin/id"}},
		"check slash":             {Op: OpCheck, Args: []string{"xbridger-doctor/../x"}},
		"check dotfile":           {Op: OpCheck, Args: []string{".hidden"}},
		"check dotdot":            {Op: OpCheck, Args: []string{".."}},
		"check upper":             {Op: OpCheck, Args: []string{"XBRIDGER-DOCTOR"}},
		"check not configured":    {Op: OpCheck, Args: []string{"remnawave-backup-age"}},
		"check nul":               {Op: OpCheck, Args: []string{"xbridger-doctor\x00"}},
		"check newline":           {Op: OpCheck, Args: []string{"xbridger-doctor\n"}},
		"check two args":          {Op: OpCheck, Args: []string{"xbridger-doctor", "--evil"}},
		"too many args":           {Op: OpCheck, Args: []string{"a", "b", "c", "d", "e"}},
		"arg too long":            {Op: OpCheck, Args: []string{strings.Repeat("a", 200)}},
	}
	for name, r := range bad {
		if err := s.Validate(r); err == nil {
			t.Errorf("%s: Validate(%+v) accepted", name, r)
		}
	}
	// Ops whose collectors are not configured are refused too.
	empty := &Server{Cfg: &agentcfg.Config{}}
	for _, r := range []Request{{Op: OpNftCounters}, {Op: OpDockerPS}, {Op: OpWGDump, Args: []string{"", "wg0"}}, {Op: OpCheck, Args: []string{"xbridger-doctor"}}} {
		if err := empty.Validate(r); err == nil {
			t.Errorf("empty config accepted %+v", r)
		}
	}
}

func TestReadRequest(t *testing.T) {
	if l, err := ReadRequest(strings.NewReader(`{"op":"docker-ps"}` + "\n" + "ignored")); err != nil || string(l) != `{"op":"docker-ps"}` {
		t.Errorf("%q %v", l, err)
	}
	if l, err := ReadRequest(strings.NewReader(`{"op":"docker-ps"}`)); err != nil || string(l) != `{"op":"docker-ps"}` {
		t.Errorf("no newline at EOF: %q %v", l, err)
	}
	if _, err := ReadRequest(strings.NewReader(strings.Repeat("x", MaxRequest+10) + "\n")); err != ErrTooLarge {
		t.Errorf("oversized: %v", err)
	}
	// Exactly at the limit (newline included) is still too large: the
	// request itself may use at most MaxRequest-1 bytes plus the newline.
	if _, err := ReadRequest(strings.NewReader(strings.Repeat("x", MaxRequest) + "\n")); err != ErrTooLarge {
		t.Errorf("at limit: %v", err)
	}
	if _, err := ReadRequest(strings.NewReader("")); err == nil {
		t.Error("empty request accepted")
	}
}

func TestDecodeRequest(t *testing.T) {
	for _, in := range []string{
		`not json`,
		`{"op":"docker-ps","cmd":"rm"}`, // unknown field
		`{"op":"docker-ps"} {"op":"nft-counters"}`,
		`{"op":"docker-ps"}garbage`,
		`["docker-ps"]`,
		`{"op":1}`,
		`{"op":"check","args":"x"}`,
	} {
		if _, err := DecodeRequest([]byte(in)); err == nil {
			t.Errorf("DecodeRequest(%q) accepted", in)
		}
	}
	if r, err := DecodeRequest([]byte(` {"op":"wg-dump","args":["","wg0"]} `)); err != nil || r.Op != OpWGDump || len(r.Args) != 2 {
		t.Errorf("%+v %v", r, err)
	}
}

func TestHandleRejectsWithoutRunning(t *testing.T) {
	f := &sysexec.Fake{}
	s := &Server{Cfg: testConfig(), Runner: f}
	for _, line := range []string{`{"op":"exec","args":["id"]}`, `{"op":"check","args":["../../bin/sh"]}`, `garbage`} {
		resp := s.Handle(context.Background(), []byte(line))
		if resp.OK || resp.Error == "" {
			t.Errorf("%s: %+v", line, resp)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("rejected requests ran commands: %+v", f.Calls)
	}
}

func TestServeConn(t *testing.T) {
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"nft -j list counters table inet vpnmap": {Stdout: fixture(t, "nft-counters.json")},
	}}
	s := &Server{Cfg: testConfig(), Runner: f}
	var out bytes.Buffer
	if err := s.ServeConn(context.Background(), strings.NewReader(`{"op":"nft-counters"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("response must be exactly one line: %q", out.String())
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || !resp.OK {
		t.Fatalf("%v %+v", err, resp)
	}
	var got []protocol.NftCounter
	json.Unmarshal(resp.Data, &got)
	if len(got) != 2 || got[0].Name != "relay_in_2053" || got[0].Bytes != 567890123456 {
		t.Errorf("got %+v", got)
	}

	out.Reset()
	s.ServeConn(context.Background(), strings.NewReader(strings.Repeat("A", 10000)), &out)
	if !strings.Contains(out.String(), `"ok":false`) || !strings.Contains(out.String(), "too large") {
		t.Errorf("oversized: %s", out.String())
	}
}

func TestDeadline(t *testing.T) {
	s := &Server{Cfg: testConfig()}
	if d := s.Deadline(Request{Op: OpNftCounters}); d != OverallTimeout {
		t.Errorf("nft deadline %s", d)
	}
	if d := s.Deadline(Request{Op: OpCheck, Args: []string{"relay-forwards"}}); d != 33*time.Second {
		t.Errorf("check deadline %s", d)
	}
	if d := CheckTimeout(agentcfg.LocalCheck{Timeout: agentcfg.Duration(time.Hour)}); d != MaxCheckTimeout {
		t.Errorf("cap %s", d)
	}
	if d := CheckTimeout(agentcfg.LocalCheck{}); d != DefaultCheckTimeout {
		t.Errorf("default %s", d)
	}
}
