package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// ------------------------------------------------------------------ secrets input

// readSecret reads a line without echo from the terminal, or a plain line
// from stdin when it is not a terminal (for scripts).
func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	line, err := stdinReader().ReadString('\n')
	if err != nil && line == "" {
		return "", failf("no input: type it on a terminal or pipe it on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

var stdinBuf *bufio.Reader

func stdinReader() *bufio.Reader {
	if stdinBuf == nil {
		stdinBuf = bufio.NewReader(os.Stdin)
	}
	return stdinBuf
}

// ------------------------------------------------------------------ login

func cmdLogin() *cobra.Command {
	return &cobra.Command{
		Use:   "login --hub URL",
		Short: "Save the hub URL and your personal API token in ~/.config/vpnmap/config.yaml",
		Long: `Asks for your personal token (input hidden), checks it with the hub
(GET /api/auth/me) and saves hub + token to ~/.config/vpnmap/config.yaml with
mode 0600. The hub stores only the token's sha256 (CLI_TOKEN_SHA256).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("login"); err != nil {
				return err
			}
			cfg := loadCLIConfig()
			hub := strings.TrimRight(firstNonEmpty(g.Hub, os.Getenv("VPNMAP_HUB"), cfg.Hub), "/")
			if hub == "" {
				return failf("usage: vpnmap login --hub https://map.example.com")
			}
			u, err := url.Parse(hub)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				return failf("hub %q must be a URL like https://map.example.com", hub)
			}
			local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
			if u.Scheme == "http" && !local {
				return failf("use https:// for %s: the token would travel in clear text", u.Host)
			}
			token := g.Token
			if token == "" {
				token, err = readSecret("Personal API token for " + hub + " (input hidden; empty for a demo hub): ")
				if err != nil {
					return err
				}
			}
			c := &client{base: hub, token: token, hc: &http.Client{Timeout: 30 * time.Second}}
			var me struct {
				Authenticated bool   `json:"authenticated"`
				AuthDisabled  bool   `json:"authDisabled"`
				Mode          string `json:"mode"`
			}
			if _, err := c.get("/api/auth/me", nil, &me); err != nil {
				return err
			}
			if !me.Authenticated {
				return failf("the hub at %s did not accept this token (nothing was saved)\nhint: the hub's CLI_TOKEN_SHA256 must contain the token's sha256", hub)
			}
			if err := saveCLIConfig(cliConfig{Hub: hub, Token: token}); err != nil {
				return err
			}
			if g.JSON {
				return printJSON(map[string]any{"hub": hub, "mode": me.Mode, "authDisabled": me.AuthDisabled, "saved": configPath()})
			}
			note := ""
			if me.AuthDisabled {
				note = amber(" (this hub has auth disabled: demo mode)")
			}
			fmt.Printf("%s logged in to %s (%s mode)%s\nsaved %s (mode 0600)\n", green("✓"), hub, orDash(me.Mode), note, configPath())
			return nil
		},
	}
}

func saveCLIConfig(c cliConfig) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append([]byte("# written by `vpnmap login`; keep it private (mode 0600)\n"), raw...), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ------------------------------------------------------------------ demo

func cmdDemo() *cobra.Command {
	c := &cobra.Command{
		Use:   "demo list|play <scenario>",
		Short: "Replay recorded incidents on a demo hub",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List the demo scenarios", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var sc []demoScenario
			if done, err := readCall(c, "/api/demo/scenarios", nil, &sc); done || err != nil {
				return err
			}
			t := newTable("SCENARIO", "DATE", "LENGTH", "LAST REPLAY", "TITLE")
			for _, s := range sc {
				last := ""
				if s.LastStart != "" {
					last = ago(s.LastStart)
				}
				if s.Running {
					last = amber("running")
				}
				t.add(bold(s.ID), s.Date, humanDur(time.Duration(s.DurationSec)*time.Second), last, s.Title)
			}
			t.print(os.Stdout)
			fmt.Println(dim("Replay one with: vpnmap demo play <scenario>"))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "play <scenario>", Short: "Start replaying a scenario now (demo hubs only)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var r struct {
				Scenario    string `json:"scenario"`
				Start       string `json:"start"`
				DurationSec int    `json:"durationSec"`
			}
			raw, err := c.post("/api/demo/play", map[string]string{"scenario": args[0]}, &r)
			if err != nil {
				return err
			}
			if g.JSON {
				return emitRaw(raw)
			}
			fmt.Printf("%s replaying %s from %s for %s\nwatch it: vpnmap status · vpnmap diagnose · vpnmap events --since 10m\n",
				green("▶"), bold(r.Scenario), localTime(r.Start), humanDur(time.Duration(r.DurationSec)*time.Second))
			return nil
		},
	})
	return c
}

// ------------------------------------------------------------------ admin

// scrypt parameters; the format matches verifyPassword in apps/hub/src/auth.ts.
const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	scryptSalt   = 16
)

// hashPassword returns scrypt$N$r$p$<salt b64>$<hash b64>.
func hashPassword(password string, salt []byte) (string, error) {
	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s", scryptN, scryptR, scryptP, base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}

// totpCode is RFC 6238 (SHA-1, 30 s, 6 digits) like totp() in auth.ts.
func totpCode(secret []byte, t time.Time) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	h := m.Sum(nil)
	o := h[len(h)-1] & 0x0f
	code := (binary.BigEndian.Uint32(h[o:o+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", code)
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func cmdAdmin() *cobra.Command {
	c := &cobra.Command{
		Use:   "admin hash-password|totp",
		Short: "Create the hub's admin password hash and TOTP secret",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(&cobra.Command{
		Use:   "hash-password",
		Short: "Read a password twice (hidden) and print ADMIN_PASSWORD_HASH (scrypt)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pw, err := readSecret("New admin password: ")
			if err != nil {
				return err
			}
			if pw == "" {
				return failf("empty password")
			}
			again, err := readSecret("Repeat it: ")
			if err != nil {
				return err
			}
			if pw != again {
				return failf("the passwords do not match; nothing was printed")
			}
			if len([]rune(pw)) < 12 {
				fmt.Fprintln(os.Stderr, amber("warning: use at least 12 characters (a passphrase) for the admin password"))
			}
			salt := make([]byte, scryptSalt)
			if _, err := rand.Read(salt); err != nil {
				return err
			}
			h, err := hashPassword(pw, salt)
			if err != nil {
				return err
			}
			if g.JSON {
				return printJSON(map[string]string{"ADMIN_PASSWORD_HASH": h})
			}
			fmt.Println(h)
			fmt.Fprintln(os.Stderr, dim("Set it in the hub env as ADMIN_PASSWORD_HASH (single-quote it in shells: it contains $; in docker compose write $$)."))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "totp",
		Short: "Create a TOTP secret (ADMIN_TOTP_SECRET) and the otpauth:// URI for an authenticator app",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := make([]byte, 20)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			secret := b32.EncodeToString(raw)
			label := url.PathEscape("VPN Topology:admin")
			uri := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30", label, secret, url.QueryEscape("VPN Topology"))
			code := totpCode(raw, time.Now())
			if g.JSON {
				return printJSON(map[string]string{"ADMIN_TOTP_SECRET": secret, "otpauth_uri": uri, "current_code": code})
			}
			fmt.Printf("ADMIN_TOTP_SECRET=%s\n\n", secret)
			fmt.Printf("Add it to your authenticator app (paste the secret, or turn this URI into a QR code locally):\n  %s\n\n", uri)
			fmt.Printf("Check: the app should show %s now (it changes every 30 s).\n", bold(code))
			fmt.Println(dim("Keep the secret only in the hub env and your authenticator; it is not stored by vpnmap."))
			return nil
		},
	})
	return c
}
