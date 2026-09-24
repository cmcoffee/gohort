package servitor

import (
	"fmt"
	"net"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"golang.org/x/crypto/ssh"
)

// --- SSH connection pool ---
// Connections are keyed by (userID, applianceID) and reused across chat/map/terminal
// calls. A connection is only closed on explicit disconnect or when it is found dead.

type sshPoolEntry struct {
	mu   sync.Mutex
	conn *ssh.Client
}

var sshConnPool sync.Map

// "userID:applianceID" → *sshPoolEntry

// acquireConn returns a live *ssh.Client for the given user+appliance, reusing
// an existing pooled connection if still alive, or dialling a fresh one.
func acquireConn(userID string, appliance Appliance) (*ssh.Client, error) {
	key := userID + ":" + appliance.ID
	if v, ok := sshConnPool.Load(key); ok {
		e := v.(*sshPoolEntry)
		e.mu.Lock()
		if connIsAlive(e.conn) {
			c := e.conn
			e.mu.Unlock()
			return c, nil
		}
		e.conn.Close()
		e.conn = nil
		e.mu.Unlock()
		sshConnPool.Delete(key)
	}

	a := &Servitor{}
	a.input.host = appliance.Host
	a.input.port = appliance.Port
	if a.input.port == 0 {
		a.input.port = 22
	}
	a.input.user = appliance.User
	if a.input.user == "" {
		a.input.user = "root"
	}
	a.input.password = appliance.Password
	// The service account's own key is the operator's identity: only an
	// administrator's appliance may borrow it. Any user can type a host,
	// user and port, and falling back to it let them sign in as the operator
	// anywhere that key is trusted, localhost included.
	a.input.no_home_key = !UserIsAdmin(appliance.Owner)
	if err := a.connect(); err != nil {
		return nil, err
	}
	e := &sshPoolEntry{conn: a.conn}
	sshConnPool.Store(key, e)
	return a.conn, nil
}

// dropConn closes and removes the pooled connection for a user+appliance.
func dropConn(userID, applianceID string) {
	key := userID + ":" + applianceID
	if v, loaded := sshConnPool.LoadAndDelete(key); loaded {
		v.(*sshPoolEntry).conn.Close()
	}
}

// connIsAlive probes an SSH client by opening and immediately closing a session.
func connIsAlive(c *ssh.Client) bool {
	if c == nil {
		return false
	}
	sess, err := c.NewSession()
	if err != nil {
		return false
	}
	sess.Close()
	return true
}

// hostKeyStore holds the SSH host keys pinned on first use, keyed
// "host:port". Set once the app's store is known (RegisterRoutes); nil (the
// command-line run) keeps the historical accept-any behaviour.
var hostKeyStore Database

const hostKeyTable = "ssh_host_keys"

// pinnedHostKey is trust on first use. The first key a host presents is
// recorded; a later connection presenting a different one is refused rather
// than handed the saved password, which is what accepting any key did for
// anybody able to sit between gohort and the machine. Saving the appliance
// again (forgetHostKey) re-trusts the host's current key, for a machine that
// was really rebuilt.
func pinnedHostKey(addr string) ssh.HostKeyCallback {
	if hostKeyStore == nil {
		return ssh.InsecureIgnoreHostKey()
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		var pinned string
		if hostKeyStore.Get(hostKeyTable, strings.ToLower(addr), &pinned) && pinned != "" {
			if pinned != fp {
				return fmt.Errorf("the SSH host key for %s has CHANGED (was %s, now %s). If the machine was rebuilt, save the system again to trust its new key; otherwise something may be intercepting the connection", addr, pinned, fp)
			}
			return nil
		}
		hostKeyStore.Set(hostKeyTable, strings.ToLower(addr), fp)
		return nil
	}
}

// forgetHostKey drops the pinned key for host:port, so the next connection
// trusts whatever the host presents.
func forgetHostKey(host string, port int) {
	if hostKeyStore == nil || strings.TrimSpace(host) == "" {
		return
	}
	if port == 0 {
		port = 22
	}
	hostKeyStore.Unset(hostKeyTable, strings.ToLower(net.JoinHostPort(host, fmt.Sprint(port))))
}
