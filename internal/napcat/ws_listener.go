package napcat

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type WSListener struct {
	wsURL    string
	token    string
	handler  func(OneBotEvent)
	conn     *websocket.Conn
	done     chan struct{}
}

func NewWSListener(wsURL, token string, handler func(OneBotEvent)) *WSListener {
	return &WSListener{
		wsURL:   wsURL,
		token:   token,
		handler: handler,
		done:    make(chan struct{}),
	}
}

func (l *WSListener) Connect() error {
	u, err := url.Parse(l.wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	if l.token != "" {
		q := u.Query()
		q.Set("access_token", l.token)
		u.RawQuery = q.Encode()
	}
	header := http.Header{}
	if l.token != "" {
		header.Set("Authorization", "Bearer "+l.token)
	}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	l.conn = conn
	log.Printf("[ws] connected to %s", l.wsURL)
	go l.readLoop()
	return nil
}

func (l *WSListener) readLoop() {
	defer close(l.done)
	for {
		_, msg, err := l.conn.ReadMessage()
		if err != nil {
			log.Printf("[ws] read error: %v", err)
			return
		}
		var ev OneBotEvent
		if err := json.Unmarshal(msg, &ev); err != nil {
			log.Printf("[ws] decode error: %v", err)
			continue
		}
		if l.handler != nil {
			l.handler(ev)
		}
	}
}

func (l *WSListener) Close() error {
	if l.conn != nil {
		_ = l.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		done := make(chan struct{})
		go func() {
			<-l.done
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		return l.conn.Close()
	}
	return nil
}
