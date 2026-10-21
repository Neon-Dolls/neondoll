package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

func main() {
	socket := flag.String("socket", "data/neondoll-admin.sock", "local neondolld administrative socket")
	flag.Parse()
	args := flag.Args()
	if len(args) < 2 || args[0] != "body" {
		fail("usage: neondollctl [-socket path] body pending|approve|deny|invite|list|revoke")
	}
	client := ownercontrol.NewClient(*socket)
	ctx := context.Background()
	var result any
	var err error
	switch args[1] {
	case "pending":
		result, err = client.ListPending(ctx)
	case "list":
		result, err = client.ListBodies(ctx)
	case "approve":
		if len(args) < 3 {
			fail("usage: body approve CODE [-permissions a,b]")
		}
		command := flag.NewFlagSet("approve", flag.ExitOnError)
		permissions := command.String("permissions", "", "comma-separated approved permissions")
		_ = command.Parse(args[3:])
		if command.NArg() != 0 {
			fail("usage: body approve CODE [-permissions a,b]")
		}
		result, err = client.Approve(ctx, args[2], split(*permissions))
	case "deny":
		if len(args) != 3 {
			fail("usage: body deny CODE")
		}
		err = client.Deny(ctx, args[2])
		result = map[string]string{"status": "denied"}
	case "invite":
		command := flag.NewFlagSet("invite", flag.ExitOnError)
		expires := command.Duration("expires", 30*time.Minute, "invitation lifetime")
		permissions := command.String("permissions", "", "comma-separated permissions")
		endpoint := command.String("endpoint", "", "Body-reachable Core bootstrap URL")
		_ = command.Parse(args[2:])
		var endpoints dollnetwork.Endpoints
		if *endpoint != "" {
			endpoints = dollnetwork.Endpoints{{URL: *endpoint}}
		}
		result, err = client.CreateInvitation(ctx, *expires, split(*permissions), endpoints)
	case "revoke":
		if len(args) != 3 {
			fail("usage: body revoke BODY_ID")
		}
		err = client.Revoke(ctx, args[2])
		result = map[string]string{"status": "revoked", "body_id": args[2]}
	default:
		fail("unknown body command")
	}
	if err != nil {
		fail(err.Error())
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fail(err.Error())
	}
}

func split(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}
