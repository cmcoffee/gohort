package core

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// A server that accepts the connection and then says nothing used to hold a
// test until net/smtp's own patience ran out. Under a context, the send ends
// when the context does, with the context's error.
func TestMailSendEndsWithItsContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open and silent.
			go func() { time.Sleep(5 * time.Second); c.Close() }()
		}
	}()
	cfg := MailConfig{Server: ln.Addr().String(), From: "test@example.test"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = cfg.SendNotification(ctx, "to@example.test", "s", "b")
	if err == nil || err != context.DeadlineExceeded {
		t.Fatalf("want the context's error, got %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("took %s — the context did not end the conversation", time.Since(started))
	}
}

// The happy path speaks the same conversation SendMail did: greeting, EHLO,
// MAIL, RCPT, DATA, the message, QUIT.
func TestMailSendSpeaksSMTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		var log strings.Builder
		say := func(s string) { c.Write([]byte(s + "\r\n")) }
		say("220 fake ready")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			log.WriteString(line)
			up := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
				say("250 fake")
			case strings.HasPrefix(up, "MAIL"), strings.HasPrefix(up, "RCPT"):
				say("250 ok")
			case up == "DATA":
				say("354 go")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						break
					}
					log.WriteString(l)
					if strings.TrimSpace(l) == "." {
						break
					}
				}
				say("250 queued")
			case up == "QUIT":
				say("221 bye")
				got <- log.String()
				return
			default:
				say("250 ok")
			}
		}
		got <- log.String()
	}()
	cfg := MailConfig{Server: ln.Addr().String(), From: "test@example.test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cfg.SendNotification(ctx, "to@example.test", "Hello", "body line"); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case log := <-got:
		for _, want := range []string{"MAIL FROM:<test@example.test>", "RCPT TO:<to@example.test>", "Subject: Hello", "body line", "QUIT"} {
			if !strings.Contains(log, want) {
				t.Fatalf("conversation missing %q:\n%s", want, log)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never saw QUIT")
	}
}
