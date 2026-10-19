// Package runtime reconstructs a live network runtime from portable network
// state.
//
// This is the Core 4 / M7.2 "the Core moves house" seam. M7.1 established
// PortableNetwork: a durable, transport-free snapshot of identity, authority,
// and memberships. This package turns a reconstructed *network.Network back
// into a running WireGuard runtime using the existing wireguard.Manager, and
// owns the runtime's lifecycle.
//
// Nothing transport-specific is reconstructed: sockets, tunnels, routes,
// endpoints, and relay mappings are all left behind by PortableNetwork.
// WireGuard peers come up with empty endpoints and learn them at runtime when
// members reach this Core.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/WireGuard"
)

// MembershipLister is the runtime's window onto persisted memberships.
// The network store in Core/Persistence satisfies this; callers can also
// provide an in-memory view (e.g. during migration, before memberships are
// re-persisted on the new host).
type MembershipLister interface {
	ListMemberships(ctx context.Context) ([]*network.Membership, error)
}

// Runtime is a reconstructed, running network runtime. It composes the
// existing WireGuard pieces — a RealTunnel and the wireguard.Manager — around
// reconstructed network state, and provides clean lifecycle management.
//
// The zero value is not usable; construct a Runtime with Reconstruct.
type Runtime struct {
	nw  *network.Network
	log *slog.Logger
	tun wireguard.Tunnel
	mgr *wireguard.Manager

	done     chan struct{}
	stopOnce sync.Once
}

// Option configures a Reconstruct call. Options are backward compatible:
// omitting them preserves the previous behavior exactly.
type Option func(*reconstructOptions)

type reconstructOptions struct {
	listenPort *int
}

// WithListenPort pins the reconstructed runtime's WireGuard tunnel to a fixed
// UDP port instead of an ephemeral one. A stable listen port is what lets an
// existing Body reconnect after Core reconstruction: the Body's durable
// endpoint (learned at pairing) remains valid, so no new endpoint
// advertisement is needed.
func WithListenPort(port int) Option {
	return func(o *reconstructOptions) {
		p := port
		o.listenPort = &p
	}
}

// Reconstruct builds and starts a live network runtime from reconstructed
// network state:
//
//   - Core identity (WireGuard keypair, overlay address, overlay prefix,
//     network ID) is taken from nw.Core;
//   - WireGuard peers are derived from store memberships through the existing
//     wireguard.Manager, which includes only active memberships — pending
//     (not yet authorized) and revoked members are excluded from the active
//     peer configuration while remaining part of the reconstructed
//     membership graph;
//   - no transport state is reconstructed: peers start with empty endpoints
//     and learn them from incoming traffic.
//
// The returned Runtime is already started. Call Stop to shut it down; Done is
// closed when shutdown completes. Stop is idempotent.
//
// Options are backward compatible: callers that pass no options get exactly
// the previous behavior (the tunnel binds an ephemeral WireGuard port).
func Reconstruct(ctx context.Context, nw *network.Network, store MembershipLister, log *slog.Logger, opts ...Option) (*Runtime, error) {
	if nw == nil {
		return nil, errors.New("runtime: nil network")
	}
	if nw.Core.PeerID == "" || nw.Core.PrivateKey == (network.WireGuardPrivateKey{}) {
		return nil, errors.New("runtime: network has no core identity")
	}
	if store == nil {
		return nil, errors.New("runtime: nil membership store")
	}
	if log == nil {
		log = slog.Default()
	}
	var o reconstructOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	tun := wireguard.NewRealTunnel(log)
	r := &Runtime{
		nw:   nw,
		log:  log,
		tun:  tun,
		done: make(chan struct{}),
	}

	mgr := wireguard.NewManager(tun, store, log)
	if o.listenPort != nil {
		mgr.SetListenPort(*o.listenPort)
	}
	if err := mgr.Start(ctx, nw); err != nil {
		// Best-effort teardown: Manager.Stop would also work here, but the
		// tunnel has only partially started and Stop alone is not guaranteed
		// to release it if Start failed mid-way.
		_ = tun.Stop()
		return nil, fmt.Errorf("runtime: reconstruct: %w", err)
	}
	r.mgr = mgr

	log.Info("runtime reconstructed",
		"network_id", nw.NetworkID,
		"overlay_prefix", nw.Core.OverlayPrefix,
	)
	return r, nil
}

// Stop shuts the runtime down. It is safe to call more than once; subsequent
// calls return nil. Done is closed when shutdown completes.
func (r *Runtime) Stop() error {
	var stopErr error
	r.stopOnce.Do(func() {
		stopErr = r.mgr.Stop()
		close(r.done)
		r.log.Info("runtime stopped", "network_id", r.nw.NetworkID)
	})
	return stopErr
}

// Done is closed when the runtime has been stopped.
func (r *Runtime) Done() <-chan struct{} {
	return r.done
}

// Network returns the reconstructed network state this runtime was built
// from. The returned value is not modified by the runtime.
func (r *Runtime) Network() *network.Network {
	return r.nw
}

// Manager exposes the underlying WireGuard manager for diagnostics and peer
// refreshes.
func (r *Runtime) Manager() *wireguard.Manager {
	return r.mgr
}

// Tunnel exposes the underlying WireGuard tunnel. Callers that need the
// tunnel's netstack (e.g. the daemon's WebSocket-over-overlay listener) use
// this accessor.
func (r *Runtime) Tunnel() wireguard.Tunnel {
	return r.tun
}

// RefreshPeers reloads the WireGuard peer configuration from the membership
// store. It returns an error if the runtime is not started.
func (r *Runtime) RefreshPeers(ctx context.Context) error {
	return r.mgr.RefreshPeers(ctx)
}

// Diagnostics returns WireGuard tunnel diagnostics. It returns an error if
// the runtime is not started.
func (r *Runtime) Diagnostics(ctx context.Context) (*wireguard.DiagnosticsResult, error) {
	return r.mgr.Diagnostics(ctx)
}
