package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

func main() {
	urlFlag := flag.String("url", "ws://127.0.0.1:8080/ws", "Doll Link WebSocket URL")
	dollID := flag.String("doll", "", "Doll ID to address")
	timeout := flag.Duration("timeout", 5*time.Second, "max time to wait for a response")
	flag.Parse()

	message := strings.Join(flag.Args(), " ")
	if message == "" {
		fmt.Fprintln(os.Stderr, "usage: neondoll-client --doll <id> [--url <ws-url>] [--timeout <duration>] <message>")
		os.Exit(1)
	}
	if *dollID == "" {
		fmt.Fprintln(os.Stderr, "error: --doll is required")
		os.Exit(1)
	}

	// Generate a unique request ID for correlation.
	reqID := uuid.New().String()

	// Build the request event.
	reqEvent := events.NewDollMessage(reqID, *dollID, message)

	// Connect via WebSocket.
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	conn, _, err := dialer.Dial(*urlFlag, http.Header{})
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	// Send the event.
	if err := conn.WriteJSON(reqEvent); err != nil {
		log.Fatalf("write: %v", err)
	}
	log.Printf("sent: doll=%q msg=%q req=%s", *dollID, message, reqID)

	// Wait for a response with matching correlation ID or type=system error.
	conn.SetReadDeadline(time.Now().Add(*timeout))

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Fatalf("read: %v (no matching response within %v)", err, *timeout)
		}

		var resp events.Event
		if err := json.Unmarshal(raw, &resp); err != nil {
			log.Printf("skipping invalid frame: %v", err)
			continue
		}

		// Accept responses matching our correlation ID, OR system events
		// (which may be errors for unknown dolls).
		if resp.CorrelationID == reqID || resp.Type == events.TypeSystem {
			pretty, _ := json.MarshalIndent(resp, "", "  ")
			fmt.Println(string(pretty))

			if resp.Type == events.TypeSystem {
				os.Exit(1)
			}
			return
		}
	}
}