package servitor

import (
	"sync"

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
