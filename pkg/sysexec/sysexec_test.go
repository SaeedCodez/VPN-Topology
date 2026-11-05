package sysexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunStdoutExitCodeAndStdin(t *testing.T) {
	res, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "cat; echo oops >&2; exit 3"}, Stdin: []byte("hello")})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || ee.Stderr != "oops" {
		t.Fatalf("err = %v", err)
	}
	if string(res.Stdout) != "hello" || res.ExitCode != 3 {
		t.Errorf("res = %+v", res)
	}
}

func TestRunMinimalEnvironment(t *testing.T) {
	t.Setenv("VPNMAP_SECRET_SHOULD_NOT_LEAK", "x")
	res, err := Exec{}.Run(context.Background(), Cmd{Name: "env"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(res.Stdout))
	if strings.Join(got, " ") != strings.Join(SafeEnv, " ") {
		t.Errorf("child env = %v, want %v", got, SafeEnv)
	}
}

func TestRunOutputLimit(t *testing.T) {
	res, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "head -c 100000 /dev/zero"}, MaxOutput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 1000 || !res.Truncated {
		t.Errorf("len=%d truncated=%v", len(res.Stdout), res.Truncated)
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	start := time.Now()
	// The background sleep holds stdout open; only a group kill ends it fast.
	_, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "sleep 30 & sleep 30"}, Timeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s, process group not killed", d)
	}
}

func TestLookPath(t *testing.T) {
	if _, err := LookPath("sh"); err != nil {
		t.Errorf("sh: %v", err)
	}
	if _, err := LookPath("./sh"); err == nil {
		t.Error("relative path must be rejected")
	}
	if _, err := LookPath("definitely-not-a-program-xyz"); err == nil {
		t.Error("missing program must fail")
	}
}

func TestFake(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"nft list tables": {Stdout: "table inet vpnmap\n"},
		"nft bad":         {ExitCode: 1, Stderr: "Error: syntax\n"},
	}}
	res, err := f.Run(context.Background(), Cmd{Name: "nft", Args: []string{"list", "tables"}})
	if err != nil || string(res.Stdout) != "table inet vpnmap\n" {
		t.Errorf("%q %v", res.Stdout, err)
	}
	var ee *ExitError
	if _, err := f.Run(context.Background(), Cmd{Name: "nft", Args: []string{"bad"}}); !errors.As(err, &ee) || ee.Stderr != "Error: syntax" {
		t.Errorf("err = %v", err)
	}
	if _, err := f.Run(context.Background(), Cmd{Name: "rm"}); err == nil {
		t.Error("unexpected command must fail")
	}
	if len(f.Calls) != 3 {
		t.Errorf("calls = %d", len(f.Calls))
	}
}
