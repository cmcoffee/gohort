package servitor

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testSSHServer accepts one public key and nothing else, and counts the
// connections that reached authentication.
func testSSHServer(t *testing.T, allowed ssh.PublicKey) (host string, port int, dials *int32) {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	var n int32
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if allowed != nil && string(key.Marshal()) == string(allowed.Marshal()) {
				return nil, nil
			}
			return nil, errTestRefused
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&n, 1)
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "test server")
				}
				sc.Close()
			}()
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	return h, port, &n
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTestRefused = testErr("refused")

// newTestKey returns a private key as PEM (with a passphrase when one is
// given) and its public half.
func newTestKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), sshPub
}

// A system signs in with its own saved key, and a key with a passphrase opens
// with the system's password, which is where the editor asks for it. Neither
// needs the server's own key, which is what lets a system that is not an
// administrator's sign in with a key at all.
func TestASystemSignsInWithItsOwnKey(t *testing.T) {
	for _, pass := range []string{"", "open sesame"} {
		key, pub := newTestKey(t, pass)
		host, port, _ := testSSHServer(t, pub)
		s := &Servitor{}
		s.input.host, s.input.port, s.input.user = host, port, "tester"
		s.input.key_pem, s.input.password, s.input.no_home_key = key, pass, true
		if err := s.connect(); err != nil {
			t.Fatalf("passphrase %q: connect: %v", pass, err)
		}
		s.conn.Close()
	}
}

// The key is checked as it is saved, so a bad paste says what is wrong
// instead of reading as the system refusing.
func TestASavedKeyThatCannotBeReadSaysWhy(t *testing.T) {
	locked, pub := newTestKey(t, "secret")
	if _, err := parseSystemKey(locked, ""); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("a key with a passphrase and no password must say so, got %v", err)
	}
	if _, err := parseSystemKey(locked, "wrong"); err == nil {
		t.Error("the wrong passphrase must not open the key")
	}
	if _, err := parseSystemKey(string(ssh.MarshalAuthorizedKey(pub)), ""); err == nil || !strings.Contains(err.Error(), ".pub") {
		t.Errorf("a pasted public key must be refused as one, got %v", err)
	}
}

// A refused sign-in is not repeated for a minute: the commands queued behind
// it fail at once with the same answer, which is what keeps a system from
// locking this machine out. Saving the system (dropConn) tries again at once.
func TestARefusedSignInIsNotRepeated(t *testing.T) {
	key, _ := newTestKey(t, "")
	host, port, dials := testSSHServer(t, nil) // accepts no key
	a := Appliance{ID: "sys-refused", Host: host, Port: port, User: "tester", PrivateKey: key}
	user := "refusal-test-user"
	t.Cleanup(func() { dropConn(user, a.ID) })
	if _, err := acquireConn(user, a); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("first sign-in should be refused, got %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := acquireConn(user, a); err == nil || !strings.Contains(err.Error(), "not retried") {
			t.Fatalf("a repeat must fail without signing in again, got %v", err)
		}
	}
	if got := atomic.LoadInt32(dials); got != 1 {
		t.Errorf("the system was dialled %d times; a refusal must hold the rest back", got)
	}
	dropConn(user, a.ID)
	if _, err := acquireConn(user, a); err == nil || strings.Contains(err.Error(), "not retried") {
		t.Fatalf("after a save the sign-in must be tried again, got %v", err)
	}
	if got := atomic.LoadInt32(dials); got != 2 {
		t.Errorf("after a save the system should be dialled again, got %d dials", got)
	}
}

// The key is write-only like the password: every read path blanks it and
// says only whether one is saved, a save that omits it keeps it, and leaving
// SSH drops it.
func TestASystemKeyIsWriteOnly(t *testing.T) {
	src := webSource(t)
	if n := strings.Count(src, `HasKey, a.PrivateKey = a.PrivateKey != "", ""`) + strings.Count(src, `resp.HasKey, resp.PrivateKey = resp.PrivateKey != "", ""`); n < 4 {
		t.Errorf("only %d read paths blank the private key; every path that blanks Password must", n)
	}
	for _, want := range []string{
		"if req.PrivateKey == \"\" && !req.ClearKey {\n\t\t\t\treq.PrivateKey = existing.PrivateKey",
		"req.HasKey, req.ClearKey = false, false",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("save path is missing %q", want)
		}
	}
}

// A system reached through a peer is checked before its session runs, in both
// the investigation and the mapping path, and a failure ends the session as a
// local one ends. Without it a refused sign-in on the far side came back as
// one more failed command and the session kept sending them, each another
// refused sign-in, until the system there locked the peer out. Asserted on the
// source: standing up a peer needs fixtures that live in core's tests.
func TestAPeerSystemIsReachedBeforeItsSessionRuns(t *testing.T) {
	for file, want := range map[string]string{
		"probe_session.go": "if err := peerReachable(pr.ctx, pr.appliance); err != nil {\n\t\t\tprobeSessions.AppendEvent(pr.id, probeEvent{Kind: \"error\", Text: \"Connection failed: \" + err.Error()}, true)",
		"map_session.go":   "if err := peerReachable(ctx, appliance); err != nil {\n\t\t\tprobeSessions.AppendEvent(id, probeEvent{Kind: \"error\", Text: \"Connection failed: \" + err.Error()}, true)",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s no longer checks a peer system before its session runs", file)
		}
	}
}

// A tool-backed system's investigation, asked for by an agent, runs its bound
// tools under that agent's credential limits, as the agent's own calls do.
func TestAToolsetRunsUnderTheActingAgentsCredentialLimits(t *testing.T) {
	raw, err := os.ReadFile("toolset.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{"if agent := ActingAgent(ctx); agent != \"\" {", "sess.DeniedCredentials = orch.AgentCredentialDeny(who, agent)"} {
		if !strings.Contains(src, want) {
			t.Errorf("toolset sessions lost %q", want)
		}
	}
}
