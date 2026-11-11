package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMailhog(t *testing.T) {
	provider, err := New("mailhog", Config{Host: "localhost", Port: 1025, From: "test@test.com"})
	assert.NoError(t, err)
	assert.NotNil(t, provider)
}

func TestNewMailhogDefaults(t *testing.T) {
	p := newMailhog(Config{})
	assert.Equal(t, "localhost:1025", p.addr)
	assert.Equal(t, "no-reply@localhost", p.from)
}

func TestNewSESWithSender(t *testing.T) {
	provider, err := New("ses", Config{Region: "sa-east-1", SESSenderEmail: "oficina@example.com"})
	assert.NoError(t, err)
	assert.NotNil(t, provider)
}

func TestNewUnknownProvider(t *testing.T) {
	provider, err := New("unknown", Config{})
	assert.Error(t, err)
	assert.Nil(t, provider)
}

func TestMailhogRequiresRecipient(t *testing.T) {
	err := newMailhog(Config{}).Send(context.Background(), Message{Subject: "s"})
	assert.ErrorContains(t, err, "recipient list is empty")
}

func TestMailhogSendsThroughSMTP(t *testing.T) {
	addr, received := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	var portNumber int
	_, _ = fmt.Sscanf(port, "%d", &portNumber)

	p := newMailhog(Config{Host: host, Port: portNumber, From: "oficina@localhost"})
	err := p.Send(context.Background(), Message{
		To: []string{"cliente@example.com"}, Subject: "Orcamento", Body: "<p>ok</p>", HTML: true,
	})
	require.NoError(t, err)

	select {
	case data := <-received:
		assert.Contains(t, data, "Subject: Orcamento")
		assert.Contains(t, data, "text/html")
		assert.Contains(t, data, "<p>ok</p>")
	case <-time.After(2 * time.Second):
		t.Fatal("smtp data not received")
	}
}

func TestMailhogReportsFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().(*net.TCPAddr)
	require.NoError(t, ln.Close())

	p := newMailhog(Config{Host: "127.0.0.1", Port: addr.Port})
	err = p.Send(context.Background(), Message{To: []string{"a@b.c"}, Subject: "s", Body: "b"})
	assert.ErrorContains(t, err, "mailhog: send failed")
}

func TestMailhogHonoursContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			time.Sleep(time.Second)
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	addr := ln.Addr().(*net.TCPAddr)
	p := newMailhog(Config{Host: "127.0.0.1", Port: addr.Port})
	err = p.Send(ctx, Message{To: []string{"a@b.c"}, Subject: "s", Body: "b"})
	assert.ErrorIs(t, err, context.Canceled)
}

func fakeSMTP(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		w("220 fake")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					received <- data.String()
					w("250 ok")
					continue
				}
				data.WriteString(line)
				continue
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case cmd == "DATA":
				inData = true
				w("354 go")
			case cmd == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String(), received
}
