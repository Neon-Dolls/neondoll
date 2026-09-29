// TestPairingRendezvous is the real M2 rendezvous test: the reference Ruby
// Body (neondoll/Body) pairs against Rin's real Core M2 implementation now on
// main (Core/Invitation + Core/Pairing /v1/pair handler) over a real HTTP
// server, using a fresh Body identity + WG keypair. It proves that the Body
// and Core agree on the canonical membership after a live rendezvous, and
// that the Body's identity + WG keypair survive pairing unchanged.
//
// This file is intentionally NOT behind a build tag so `go test -race ./...`
// exercises the real Body↔Core rendezvous on every run.
package integration

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	corenetwork "github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
)

// testNetStore opens a real SQLite-backed Core NetworkStore in a temp dir.
func testNetStore(t *testing.T, dir string) corenetwork.NetworkStore {
	t.Helper()
	s, err := persistence.NewStore(dir + "/core_state.db")
	if err != nil {
		t.Fatalf("persistence.NewStore: %v", err)
	}
	ns, ok := s.(corenetwork.NetworkStore)
	if !ok {
		t.Fatalf("persistence Store %T does not implement network.NetworkStore", s)
	}
	return ns
}

// TestM2BodyPairsWithRealCoreRendezvous proves the full live rendezvous using
// real Core invitation + pairing services and the real Ruby Body.
func TestM2BodyPairsWithRealCoreRendezvous(t *testing.T) {
	// ── Real Core: network + SQLite-backed persistence store ────────────
	netID := corenetwork.NetworkID("live-rendezvous-net-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	net, err := corenetwork.NewNetwork(netID)
	if err != nil {
		t.Fatalf("network.NewNetwork: %v", err)
	}
	coreNetStore := testNetStore(t, t.TempDir())

	// ── Real Core: invitation service (fresh, real clock) ───────────────
	clock := invitation.RealClock{}
	invStore := invitation.NewMemoryStore()
	invSvc := invitation.NewService(invStore, clock)

	// ── Real Core: pairing service + real /v1/pair HTTP handler ────────
	allowAll := pairing.AllowAuthorizer{}
	coreEndpoints := dollnetwork.Endpoints{{URL: "http://core-endpoint.invalid"}}
	pairingSvc := pairing.NewService(net, coreNetStore, invSvc, allowAll, clock, coreEndpoints)
	handler := pairing.NewHandler(pairingSvc, slog.Default())

	srv := httptest.NewServer(http.HandlerFunc(handler.HandlePair))
	defer srv.Close()

	// ── Core creates a real invitation pointing back at its own origin ──
	created, err := invSvc.Create(context.Background(), invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: dollnetwork.Endpoints{{URL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("invSvc.Create: %v", err)
	}
	inv := &dollnetwork.Invitation{
		Version:            dollnetwork.ProtocolVersion,
		InvitationID:       created.ID,
		InvitationSecret:   created.Secret,
		ExpiresAt:          created.ExpiresAt.Format(time.RFC3339),
		BootstrapEndpoints: created.BootstrapEndpoints,
	}

	// ── Ruby Body: fresh persistent store (identity + WG keypair) ───────
	store := body.NewStore(t.TempDir() + "/body")
	if _, err := store.CreateFresh("RendezvousBody", body.BodyMetadata{
		Implementation: "neondoll-body/m2",
		Platform:       "test",
		Arch:           "test",
	}); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	stBefore, kpBefore, err := store.LoadOrError()
	if err != nil || stBefore == nil || kpBefore == nil {
		t.Fatalf("LoadOrError before pairing: %v", err)
	}
	bodyIDBefore := string(stBefore.Identity.BodyID)
	wgPubBefore := kpBefore.PublicKeyBase64()

	// ── Live rendezvous: Body posts a canonical PairRequest to real Core ─
	now := clock.Now().Unix()
	res, err := body.PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation against real Core: %v", err)
	}
	if res == nil || res.Membership == nil {
		t.Fatal("expected non-nil membership from live rendezvous")
	}
	m := res.Membership

	// ── State read back exactly as Core the address-authority assigned ──
	coreMember := net.GetMembership(corenetwork.PeerID(m.BodyPeerID))
	if coreMember == nil {
		t.Fatalf("Core does not know body_peer_id %q", m.BodyPeerID)
	}

	// Agreement on the network ID.
	if m.NetworkID != string(net.NetworkID) {
		t.Errorf("network_id: Body=%q Core=%q", m.NetworkID, net.NetworkID)
	}

	// Agreement on the Body peer ID.
	if string(coreMember.PeerID) != m.BodyPeerID {
		t.Errorf("body peer id: Body=%q Core=%q", m.BodyPeerID, coreMember.PeerID)
	}
	// Agreement on the Body overlay IPv6 (bare address, no /mask).
	if m.BodyIPv6 != coreMember.OverlayAddress.String() {
		t.Errorf("body_ipv6: Body=%q Core=%q", m.BodyIPv6, coreMember.OverlayAddress.String())
	}

	// Agreement on the Core peer ID.
	if m.CorePeerID != string(net.Core.PeerID) {
		t.Errorf("core_peer_id: Body=%q Core=%q", m.CorePeerID, net.Core.PeerID)
	}
	// Agreement on the Core WG public key.
	coreWG, err := dollnetwork.EncodeWgPublicKey(net.Core.PublicKey[:])
	if err != nil {
		t.Fatalf("EncodeWgPublicKey(core pub): %v", err)
	}
	if m.CoreWGKeyB64 != coreWG {
		t.Errorf("core_wg_public_key: Body=%q Core=%q", m.CoreWGKeyB64, coreWG)
	}

	if m.BodyIPv6 == "" || m.CorePeerID == "" || m.CoreWGKeyB64 == "" {
		t.Error("membership missing a required rendezvous field")
	}

	// ── Core created + activated the membership ─────────────────────────
	if coreMember.Status != corenetwork.MembershipActive {
		t.Errorf("Core membership status = %v, want active", coreMember.Status)
	}

	// ── Body identity + WG keypair survive the rendezvous unchanged ─────
	stAfter, kpAfter, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError after pairing: %v", err)
	}
	if string(stAfter.Identity.BodyID) != bodyIDBefore {
		t.Errorf("Body ID changed across rendezvous: %q → %q", bodyIDBefore, string(stAfter.Identity.BodyID))
	}
	if kpAfter.PublicKeyBase64() != wgPubBefore {
		t.Errorf("WG public key changed across rendezvous")
	}

	// ── Durable membership agrees with the returned membership ──────────
	persisted, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if persisted.NetworkID != m.NetworkID || persisted.BodyPeerID != m.BodyPeerID {
		t.Errorf("durable membership %+v does not match returned membership %+v", persisted, m)
	}
}
