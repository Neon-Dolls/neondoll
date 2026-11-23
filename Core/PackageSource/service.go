package packagesource

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

const serviceStateKey = "package-sources/proposals-v1"

type StateStore interface {
	Get(context.Context, string, string) ([]byte, error)
	Put(context.Context, string, string, []byte) error
}

type ProposalState string

const (
	ProposalPending   ProposalState = "pending"
	ProposalExecuting ProposalState = "executing"
	ProposalAccepted  ProposalState = "accepted"
	ProposalRejected  ProposalState = "rejected"
	ProposalFailed    ProposalState = "failed"
)

type StoredProposal struct {
	Proposal Proposal        `json:"proposal"`
	State    ProposalState   `json:"state"`
	Report   ExecutionReport `json:"report,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type serviceSnapshot struct {
	Cursors   map[string]string         `json:"cursors"`
	Proposals map[string]StoredProposal `json:"proposals"`
}

type Service struct {
	mu            sync.Mutex
	registry      *Registry
	executor      *Executor
	state         StateStore
	stateInstance string
	snapshot      serviceSnapshot
	cancel        context.CancelFunc
	done          chan struct{}
}

func NewService(ctx context.Context, registry *Registry, executor *Executor, state StateStore, stateInstance string) (*Service, error) {
	if registry == nil || executor == nil || state == nil || stateInstance == "" {
		return nil, ErrInvalid
	}
	s := &Service{registry: registry, executor: executor, state: state, stateInstance: stateInstance, snapshot: serviceSnapshot{Cursors: map[string]string{}, Proposals: map[string]StoredProposal{}}}
	data, err := state.Get(ctx, stateInstance, serviceStateKey)
	if err == nil {
		if json.Unmarshal(data, &s.snapshot) != nil {
			return nil, ErrInvalid
		}
	} else if !errors.Is(err, ErrStateNotFound) {
		return nil, err
	}
	if s.snapshot.Cursors == nil {
		s.snapshot.Cursors = map[string]string{}
	}
	if s.snapshot.Proposals == nil {
		s.snapshot.Proposals = map[string]StoredProposal{}
	}
	return s, nil
}

func (s *Service) persist(ctx context.Context) error {
	data, err := json.Marshal(s.snapshot)
	if err != nil {
		return err
	}
	return s.state.Put(ctx, s.stateInstance, serviceStateKey, data)
}

func (s *Service) Poll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acknowledgments := map[string][]string{}
	for _, id := range s.registry.IDs() {
		source, ok := s.registry.Source(id)
		if !ok {
			continue
		}
		result, err := source.Proposals(ctx, ProposalRequest{Cursor: s.snapshot.Cursors[id], Limit: 32})
		if err != nil {
			return err
		}
		for _, proposal := range result.Proposals {
			if proposal.ID == "" || proposal.Plan.ProposalID != proposal.ID {
				return ErrInvalid
			}
			key := id + "\x00" + proposal.ID
			if _, exists := s.snapshot.Proposals[key]; !exists {
				s.snapshot.Proposals[key] = StoredProposal{Proposal: proposal, State: ProposalPending}
			}
			acknowledgments[id] = append(acknowledgments[id], proposal.ID)
		}
		if result.Cursor != "" {
			s.snapshot.Cursors[id] = result.Cursor
		}
	}
	if err := s.persist(ctx); err != nil {
		return err
	}
	for sourceID, ids := range acknowledgments {
		source, ok := s.registry.Source(sourceID)
		if !ok {
			continue
		}
		if err := source.AcknowledgeProposals(ctx, ids); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Start(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return ErrInvalid
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return ErrConflict
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = s.Poll(runCtx)
			}
		}
	}()
	return nil
}

func (s *Service) Proposals() []StoredProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]StoredProposal, 0, len(s.snapshot.Proposals))
	for _, proposal := range s.snapshot.Proposals {
		result = append(result, proposal)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Proposal.ID < result[j].Proposal.ID })
	return result
}

func (s *Service) Accept(ctx context.Context, sourceID, proposalID string) (ExecutionReport, error) {
	key := sourceID + "\x00" + proposalID
	s.mu.Lock()
	stored, ok := s.snapshot.Proposals[key]
	if !ok || stored.State != ProposalPending {
		s.mu.Unlock()
		return ExecutionReport{}, ErrConflict
	}
	stored.State = ProposalExecuting
	s.snapshot.Proposals[key] = stored
	if err := s.persist(ctx); err != nil {
		s.mu.Unlock()
		return ExecutionReport{}, err
	}
	s.mu.Unlock()
	report, err := s.executor.ExecuteWithProgress(ctx, stored.Proposal.Plan, func(progress ExecutionReport) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		current := s.snapshot.Proposals[key]
		current.Report = progress
		s.snapshot.Proposals[key] = current
		return s.persist(ctx)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	stored = s.snapshot.Proposals[key]
	stored.Report = report
	if err != nil {
		stored.State, stored.Error = ProposalFailed, err.Error()
	} else {
		stored.State = ProposalAccepted
	}
	s.snapshot.Proposals[key] = stored
	if persistErr := s.persist(ctx); persistErr != nil {
		err = errors.Join(err, persistErr)
	}
	return report, err
}

func (s *Service) Reject(ctx context.Context, sourceID, proposalID, reason string) error {
	key := sourceID + "\x00" + proposalID
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.snapshot.Proposals[key]
	if !ok || stored.State != ProposalPending {
		return ErrConflict
	}
	stored.State, stored.Error = ProposalRejected, reason
	s.snapshot.Proposals[key] = stored
	return s.persist(ctx)
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
