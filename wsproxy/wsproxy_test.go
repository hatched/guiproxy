package wsproxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/gorilla/websocket"

	"github.com/juju/guiproxy/wsproxy"
)

func TestCopy(t *testing.T) {
	c := qt.New(t)
	defer c.Cleanup()
	// Set up a target WebSocket server.
	ping := httptest.NewServer(http.HandlerFunc(pingHandler))
	defer ping.Close()

	// Set up the WebSocket proxy that copies the messages back and forth.
	conn1Log, conn2Log := &logStorage{}, &logStorage{}
	proxy := httptest.NewServer(newProxyHandler(wsURL(ping.URL), conn1Log, conn2Log))
	defer proxy.Close()

	// Connect to the proxy.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(proxy.URL), nil)
	c.Assert(err, qt.Equals, nil)

	// Send messages and check that ping responses are properly received.
	send := func(content string) string {
		msg := jsonMessage{
			Content: content,
		}
		err = conn.WriteJSON(msg)
		c.Assert(err, qt.Equals, nil)
		err = conn.ReadJSON(&msg)
		c.Assert(err, qt.Equals, nil)
		return msg.Content
	}
	c.Assert(send("ping"), qt.Equals, "ping pong")
	c.Assert(send("bad wolf"), qt.Equals, "bad wolf pong")

	// Incoming and outgoing WebSocket traffic has been logged.
	assertLogs := func(ls *logStorage, expected ...string) {
		// Wait for messages to be collected.
		waitForMessages(ls, len(expected))
		messages := make([]string, len(expected))
		for i, content := range expected {
			b, err := json.Marshal(jsonMessage{
				Content: content,
			})
			c.Assert(err, qt.Equals, nil)
			messages[i] = string(b)
		}
		c.Assert(ls.messages, qt.DeepEquals, messages)
	}
	assertLogs(conn1Log, "ping", "bad wolf")
	assertLogs(conn2Log, "ping pong", "bad wolf pong")
}

func waitForMessages(ls *logStorage, expectedNum int) {
	tick := time.Tick(100 * time.Millisecond)
	timeout := time.After(1 * time.Second)
	for {
		select {
		case <-timeout:
			return
		case <-tick:
			if len(ls.messages) == expectedNum {
				return
			}
		}
	}
}

// pingHandler is a WebSocket handler responding to pings.
func pingHandler(w http.ResponseWriter, req *http.Request) {
	conn := upgrade(w, req)
	defer conn.Close()
	var msg jsonMessage
	for {
		err := conn.ReadJSON(&msg)
		if err == io.EOF {
			return
		}
		if err != nil {
			panic(err)
		}
		msg.Content += " pong"
		if err = conn.WriteJSON(msg); err != nil {
			panic(err)
		}
	}
}

// newCopyHandler returns a WebSocket handler copying from the given WebSocket
// server.
func newProxyHandler(srvURL string, conn1Log, conn2Log *logStorage) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn1 := upgrade(w, req)
		conn2, _, err := websocket.DefaultDialer.Dial(srvURL, nil)
		if err != nil {
			panic(err)
		}
		if err := wsproxy.Copy(conn1, conn2, conn1Log, conn2Log); err != nil {
			panic(err)
		}
	})
}

// logStorage is a logger.Interface used for testing purposes.
type logStorage struct {
	sync.Mutex
	messages []string
}

// Print implements logger.Interface and stores log messages.
func (ls *logStorage) Print(msg string) {
	ls.Lock()
	ls.messages = append(ls.messages, msg)
	ls.Unlock()
}

// wsURL returns a WebSocket URL from the given HTTP URL.
func wsURL(u string) string {
	return strings.Replace(u, "http://", "ws://", 1)
}

// upgrade upgrades the given request and returns the resulting WebSocket
// connection.
func upgrade(w http.ResponseWriter, req *http.Request) *websocket.Conn {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		panic(err)
	}
	return conn
}

// upgrader holds a zero valued WebSocket upgrader.
var upgrader = websocket.Upgrader{}

// jsonMessage holds messages used for testing the WebSocket handlers.
type jsonMessage struct {
	Content string
}
