// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"sync"
	"testing"
)

func TestRecoveryEpoch_NextGenAdvances(t *testing.T) {
	e := NewRecoveryEpoch()
	g1 := e.NextGen()
	g2 := e.NextGen()
	if g1 == 0 || g2 <= g1 {
		t.Fatalf("NextGen must advance: got g1=%d, g2=%d", g1, g2)
	}
}

func TestRecoveryEpoch_CommitAndActive(t *testing.T) {
	e := NewRecoveryEpoch()
	gen := e.NextGen()

	tun := NewBodyTunnel(BodyTunnelConfig{}, nil)
	if !e.TryCommit(gen, tun) {
		t.Fatal("TryCommit should succeed for current generation")
	}
	if e.Active() != tun {
		t.Fatal("Active should return the committed tunnel")
	}
}

func TestRecoveryEpoch_StaleGenRejected(t *testing.T) {
	e := NewRecoveryEpoch()

	gen1 := e.NextGen()
	gen2 := e.NextGen() // supersedes gen1

	tun1 := NewBodyTunnel(BodyTunnelConfig{}, nil)
	if e.TryCommit(gen1, tun1) {
		t.Fatal("TryCommit for stale generation should be rejected")
	}

	tun2 := NewBodyTunnel(BodyTunnelConfig{}, nil)
	if !e.TryCommit(gen2, tun2) {
		t.Fatal("TryCommit for current generation should succeed")
	}

	if e.Active() != tun2 {
		t.Fatal("Active should be the tunnel committed by current generation")
	}
}

func TestRecoveryEpoch_IsCurrent(t *testing.T) {
	e := NewRecoveryEpoch()
	gen1 := e.NextGen()
	e.NextGen() // advance
	if e.IsCurrent(gen1) {
		t.Fatal("IsCurrent should return false for stale generation")
	}
}

func TestRecoveryEpoch_ShutdownRejectsNextGen(t *testing.T) {
	e := NewRecoveryEpoch()
	e.Shutdown()
	if gen := e.NextGen(); gen != 0 {
		t.Fatalf("NextGen after shutdown should return 0, got %d", gen)
	}
}

func TestRecoveryEpoch_ShutdownClosesActiveTunnel(t *testing.T) {
	e := NewRecoveryEpoch()
	gen := e.NextGen()
	tun := NewBodyTunnel(BodyTunnelConfig{}, nil)
	e.TryCommit(gen, tun)

	e.Shutdown()

	if e.Active() != nil {
		t.Fatal("Active should be nil after shutdown")
	}
}

func TestRecoveryEpoch_CommitReplacesPreviousActive(t *testing.T) {
	e := NewRecoveryEpoch()

	gen1 := e.NextGen()
	tun1 := NewBodyTunnel(BodyTunnelConfig{}, nil)
	if !e.TryCommit(gen1, tun1) {
		t.Fatal("gen1 should commit")
	}

	gen2 := e.NextGen()
	tun2 := NewBodyTunnel(BodyTunnelConfig{}, nil)
	if !e.TryCommit(gen2, tun2) {
		t.Fatal("gen2 should commit")
	}

	if e.Active() != tun2 {
		t.Fatal("Active should be tun2 after gen2 commits")
	}
}

func TestRecoveryEpoch_ConcurrentAccess(t *testing.T) {
	e := NewRecoveryEpoch()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gen := e.NextGen()
			tun := NewBodyTunnel(BodyTunnelConfig{}, nil)
			if e.TryCommit(gen, tun) {
				if e.Active() == nil {
					t.Error("Active() should not be nil after successful commit")
				}
			}
		}()
	}
	wg.Wait()

	if e.Active() == nil {
		t.Fatal("epoch should have an active tunnel after concurrent commits")
	}
}

func TestRecoveryEpoch_IsCurrentReturnsFalseAfterShutdown(t *testing.T) {
	e := NewRecoveryEpoch()
	gen := e.NextGen()
	e.Shutdown()

	if e.IsCurrent(gen) {
		t.Fatal("IsCurrent should return false after shutdown")
	}
}

func TestRecoveryEpoch_NextGenCancelOnShutdown(t *testing.T) {
	e := NewRecoveryEpoch()
	e.Shutdown()
	for i := 0; i < 5; i++ {
		if gen := e.NextGen(); gen != 0 {
			t.Fatalf("all NextGen calls after shutdown should return 0, got %d on iteration %d", gen, i)
		}
	}
}
