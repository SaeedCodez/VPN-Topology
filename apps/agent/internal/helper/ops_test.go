package helper

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"vpnmap/pkg/protocol"
	"vpnmap/pkg/sysexec"
)

const (
	fakePrivateKey   = "cHJpdmF0ZS1rZXktbXVzdC1uZXZlci1sZWFrLTAwMDE="
	fakePresharedKey = "cHJlc2hhcmVkLWtleS1tdXN0LW5ldmVyLWxlYWstMDE="
)

func TestParseNftCounters(t *testing.T) {
	got, err := ParseNftCounters([]byte(fixture(t, "nft-counters.json")))
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.NftCounter{
		{Name: "relay_in_2053", Bytes: 567890123456, Packets: 1234},
		{Name: "wg_out", Bytes: 2000, Packets: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if _, err := ParseNftCounters([]byte("Error: oops")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestNftTableMissing(t *testing.T) {
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"nft -j list counters table inet vpnmap": {ExitCode: 1, Stderr: "Error: No such file or directory\nlist counters table inet vpnmap\n"},
	}}
	resp := (&Server{Cfg: testConfig(), Runner: f}).Handle(context.Background(), []byte(`{"op":"nft-counters"}`))
	if resp.OK || !strings.Contains(resp.Error, "nft-apply") {
		t.Errorf("%+v", resp)
	}
}

func TestWGDumpNeverLeaksKeys(t *testing.T) {
	dump := fixture(t, "wg-dump.txt")
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"wg show wg0 dump":                         {Stdout: dump},
		"ip netns exec mullvad wg show wg-mv dump": {Stdout: dump},
	}}
	s := &Server{Cfg: testConfig(), Runner: f}
	for _, req := range []string{`{"op":"wg-dump","args":["","wg0"]}`, `{"op":"wg-dump","args":["mullvad","wg-mv"]}`} {
		resp := s.Handle(context.Background(), []byte(req))
		raw, _ := json.Marshal(resp)
		if !resp.OK {
			t.Fatalf("%s: %+v", req, resp)
		}
		for _, secret := range []string{fakePrivateKey, fakePresharedKey, "cHJpdmF0", "cHJlc2hh"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s: response leaks %q: %s", req, secret, raw)
			}
		}
		var w protocol.WGInterface
		json.Unmarshal(resp.Data, &w)
		if w.Listen != 51820 || len(w.Peers) != 2 {
			t.Fatalf("%+v", w)
		}
		if w.Peers[0] != (protocol.WGPeer{Key: "UGVlck9u", Endpoint: "203.0.113.5:51820", LastHandshake: 1767225000, RxB: 123456789, TxB: 987654321}) {
			t.Errorf("peer 0 = %+v", w.Peers[0])
		}
		if w.Peers[1].Endpoint != "" || w.Peers[1].LastHandshake != 0 || len(w.Peers[1].Key) != 8 {
			t.Errorf("peer 1 = %+v", w.Peers[1])
		}
	}
	if f.Calls[1].Name != "ip" {
		t.Errorf("netns dump must go through ip netns exec: %+v", f.Calls[1])
	}
}

func TestParseWGDumpErrorsDoNotQuoteInput(t *testing.T) {
	bad := fakePrivateKey + "\tpub\n" // 2 fields instead of 4
	_, err := ParseWGDump([]byte(bad), "", "wg0")
	if err == nil || strings.Contains(err.Error(), fakePrivateKey) {
		t.Errorf("err = %v", err)
	}
	bad = fakePrivateKey + "\tpub\t1\toff\n" + "peer\t" + fakePresharedKey + "\tx\n"
	_, err = ParseWGDump([]byte(bad), "", "wg0")
	if err == nil || strings.Contains(err.Error(), fakePresharedKey) {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseWGDump(nil, "", "wg0"); err == nil {
		t.Error("empty dump accepted")
	}
}

func TestWGDumpBufferZeroed(t *testing.T) {
	buf := []byte(fixture(t, "wg-dump.txt"))
	if _, err := ParseWGDump(buf, "", "wg0"); err != nil {
		t.Fatal(err)
	}
	zero(buf)
	if strings.Contains(string(buf), fakePrivateKey) {
		t.Error("zero() left the key in memory")
	}
}

func TestDockerPS(t *testing.T) {
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{}}
	args := append([]string{"inspect", "--type", "container", "--format", DockerTemplate, "--"}, "backend-app-1", "remnawave", "gone")
	// docker exits 1 because "gone" is missing, but prints the others.
	f.Responses[sysexec.Key(sysexec.Cmd{Name: "docker", Args: args})] = sysexec.FakeResponse{
		Stdout: fixture(t, "docker-inspect.txt"), ExitCode: 1, Stderr: "Error: No such container: gone\n",
	}
	resp := (&Server{Cfg: testConfig(), Runner: f}).Handle(context.Background(), []byte(`{"op":"docker-ps"}`))
	if !resp.OK {
		t.Fatalf("%+v", resp)
	}
	var got []protocol.Container
	json.Unmarshal(resp.Data, &got)
	want := []protocol.Container{
		{Name: "backend-app-1", State: "running", Health: "healthy", StartedAt: "2026-10-30T08:00:00.123456789Z", Image: "xbridger/backend:1.4.2"},
		{Name: "remnawave", State: "restarting", StartedAt: "2026-11-01T19:59:00Z", RestartCount: 7, Image: "remnawave/backend:latest"},
		{Name: "gone", State: StateNotFound},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	// The CLI environment must not point at anyone's docker config.
	env := strings.Join(f.Calls[0].Env, " ")
	if !strings.Contains(env, "DOCKER_CONFIG=/nonexistent") {
		t.Errorf("env = %s", env)
	}
}

func TestDockerTemplateSelectsOnlySafeFields(t *testing.T) {
	for _, forbidden := range []string{"Env", "Mounts", "Labels", "Args", "Cmd", "Entrypoint", "HostConfig", "NetworkSettings", "{{json .}}"} {
		if strings.Contains(DockerTemplate, forbidden) {
			t.Errorf("template references %s", forbidden)
		}
	}
	// Extra fields in docker output (e.g. a future template mistake) are
	// dropped by the parser rather than passed through.
	line := `{"name":"/x","state":"running","health":"","started_at":"","restart_count":0,"image":"i","env":["SECRET=1"]}`
	got, err := ParseDockerInspect([]byte(line), []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "SECRET") {
		t.Errorf("leaked env: %s", raw)
	}
}

func TestDockerDaemonDown(t *testing.T) {
	args := append([]string{"inspect", "--type", "container", "--format", DockerTemplate, "--"}, "backend-app-1", "remnawave", "gone")
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		sysexec.Key(sysexec.Cmd{Name: "docker", Args: args}): {ExitCode: 1, Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n"},
	}}
	resp := (&Server{Cfg: testConfig(), Runner: f}).Handle(context.Background(), []byte(`{"op":"docker-ps"}`))
	if resp.OK || !strings.Contains(resp.Error, "Cannot connect") {
		t.Errorf("%+v", resp)
	}
}
