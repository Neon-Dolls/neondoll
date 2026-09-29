// Command: neondoll-body — headless reference Body.
//
// This command is intentionally THIN. All reusable logic lives in the
// Body package (github.com/Neon-Dolls/neondoll/Body); this
// command only parses flags and prints runtime output, so future desktop and
// mobile Bodies can reuse the same runtime without a command-line shell.
//
// M1 actions:
//   --init               create a fresh Body identity + WG keypair (once)
//   --status             print the persisted identity + WG public key
//   --pairing-request    print the M1 Doll Network pairing request
//
//   --state-dir <dir>    where identity/key state lives (default: .neondoll-body)
//   --name <name>        optional human-readable name (used at init)
//   --implementation/--platform/--arch
//                        metadata recorded at init

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Neon-Dolls/neondoll/Body"
)

func main() {
	stateDir := flag.String("state-dir", ".neondoll-body", "directory for Body identity/state")
	name := flag.String("name", "", "optional human-readable Body name")
	implementation := flag.String("implementation", "neondoll-body", "implementation identifier")
	platform := flag.String("platform", "unknown", "platform identifier")
	arch := flag.String("arch", "unknown", "architecture identifier")
	doInit := flag.Bool("init", false, "create a fresh Body identity + WG keypair")
	doStatus := flag.Bool("status", false, "print persisted identity + public key")
	doPairing := flag.Bool("pairing-request", false, "print the M1 Doll Network pairing request")
	invid := flag.String("invitation-id", "", "pairing invitation id (for pairing-request)")
	secret := flag.String("secret", "", "pairing invitation secret (for pairing-request)")
	flag.Parse()

	actions := 0
	for _, b := range []*bool{doInit, doStatus, doPairing} {
		if *b {
			actions++
		}
	}
	if actions != 1 {
		usage()
		os.Exit(1)
	}

	store := body.NewStore(*stateDir)

	if *doInit {
		meta := body.BodyMetadata{
			Implementation: *implementation,
			Platform:       *platform,
			Arch:           *arch,
		}
		// Fresh semantics: CreateFresh enforces create-once. If identity
		// already exists it returns ErrIdentityExists rather than silently
		// regenerating.
		res, err := store.CreateFresh(*name, meta)
		if err != nil {
			if err == body.ErrIdentityExists {
				fmt.Fprintf(os.Stderr, "error: identity already exists at %s (use a fresh --state-dir to re-init)\n", store.Dir())
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "init failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("created body_id=%s\n", res.State.Identity.BodyID)
		fmt.Printf("wg_public_key=%s\n", res.Key.PublicKeyBase64())
		return
	}

	st, kp, err := store.LoadOrError()
	if err != nil {
		if err == body.ErrStateNotFound {
			fmt.Fprintln(os.Stderr, "no Body identity yet — run with --init first")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "failed to load Body state (fails safe): %v\n", err)
		os.Exit(1)
	}

	if *doStatus {
		fmt.Printf("body_id: %s\n", st.Identity.BodyID)
		if st.Identity.Name != "" {
			fmt.Printf("name: %s\n", st.Identity.Name)
		}
		fmt.Printf("implementation: %s\n", st.Identity.Meta.Implementation)
		fmt.Printf("platform: %s\n", st.Identity.Meta.Platform)
		fmt.Printf("arch: %s\n", st.Identity.Meta.Arch)
		fmt.Printf("wireguard_public_key: %s\n", kp.PublicKeyBase64())
		return
	}

	if *doPairing {
		req := body.BuildPairRequest(*invid, *secret, st, kp)
		if err := req.ValidatePairRequest(); err != nil {
			fmt.Fprintf(os.Stderr, "invalid pairing request: %v\n", err)
			os.Exit(1)
		}
		wire, err := req.ToJSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "pairing request serialization failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(wire)
		var parsed map[string]any
		if jerr := json.Unmarshal([]byte(wire), &parsed); jerr != nil {
			fmt.Fprintf(os.Stderr, "internal error: generated non-JSON pairing request: %v\n", jerr)
			os.Exit(1)
		}
		return
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.Join([]string{
		"usage: neondoll-body (--init | --status | --pairing-request)",
		"        [--state-dir <dir>] [--name <name>]",
		"        [--implementation <id>] [--platform <os>] [--arch <cpu>]",
		"        [--invitation-id <id>] [--secret <sec>]",
	}, "\n"))
}
