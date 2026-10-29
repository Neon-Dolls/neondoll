package dollmind

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	actionpolicy "github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	communication "github.com/Neon-Dolls/neondoll/Core/Communication"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	permissionrequest "github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	sessionevent "github.com/Neon-Dolls/neondoll/Core/SessionEvent"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type communicationSender struct {
	requests []communication.SendTextRequest
	peers    []communication.Peer
	result   communication.Result
	err      error
}

func (s *communicationSender) ListPeers(context.Context) ([]communication.Peer, error) {
	return append([]communication.Peer(nil), s.peers...), nil
}

func TestCommunicationPendingResultIsExplicitlyDurablyQueued(t *testing.T) {
	executor := NewToolExecutor(body.NewRegistry(), body.NewGuard(body.NewRegistry(), &recordingAuthority{}), &permissionPolicy{decision: actionpolicy.DecisionAllow}, "doll")
	sender := &communicationSender{result: communication.Result{DeliveryID: "stable-delivery", Status: sessionevent.DeliveryPending}, err: errors.New("gateway offline")}
	executor.SetCommunicationSender(sender)
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "pending-send", Arguments: map[string]any{"recipient_principal_id": "owner", "text": "queued once"}}, ptrTool(communicationTool()))
	if result.Status != inference.ToolResultFailure || result.Error == nil || result.Error.Code != "delivery_pending" || result.ExecutionID != "stable-delivery" {
		t.Fatalf("result=%#v", result)
	}
	if result.Result["delivery_id"] != "stable-delivery" || result.Result["delivery_status"] != "pending" || result.Result["durably_queued"] != true || result.Result["resend_required"] != false {
		t.Fatalf("structured result=%#v", result.Result)
	}
	if len(sender.requests) != 1 {
		t.Fatalf("requests=%d", len(sender.requests))
	}
}

func (s *communicationSender) SendText(_ context.Context, request communication.SendTextRequest) (communication.Result, error) {
	s.requests = append(s.requests, request)
	return s.result, s.err
}

func TestCommunicationToolUsesPolicyAndPreservesCorrelation(t *testing.T) {
	policy := &permissionPolicy{decision: actionpolicy.DecisionAllow}
	executor := NewToolExecutor(body.NewRegistry(), body.NewGuard(body.NewRegistry(), &recordingAuthority{}), policy, "doll")
	sender := &communicationSender{result: communication.Result{DeliveryID: "delivery-1", Status: sessionevent.DeliveryAccepted}}
	executor.SetCommunicationSender(sender)
	result := executor.ExecuteCorrelatedCall(context.Background(), cognitiveexecution.ToolCorrelation{CognitiveRunID: "run-1", IntentionID: "intent-1"}, inference.ToolCall{
		ID: "send-1", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "owner-1", "text": "hello"},
	}, ptrTool(communicationTool()))
	if result.Status != inference.ToolResultSuccess || result.ExecutionID != "delivery-1" || len(sender.requests) != 1 {
		t.Fatalf("result=%#v requests=%#v", result, sender.requests)
	}
	if sender.requests[0].RecipientPrincipalID != "owner-1" || sender.requests[0].Correlation.IntentionID != "intent-1" {
		t.Fatalf("request=%#v", sender.requests[0])
	}
	policy.set(actionpolicy.DecisionDeny)
	denied := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "send-2", Arguments: map[string]any{"recipient_principal_id": "owner-1", "text": "blocked"}}, ptrTool(communicationTool()))
	if denied.Error == nil || denied.Error.Code != "action_denied" || len(sender.requests) != 1 {
		t.Fatalf("denied=%#v requests=%d", denied, len(sender.requests))
	}
}

func TestCommunicationPeerDiscoveryThenSendUsesTrustedPrincipal(t *testing.T) {
	policy := &permissionPolicy{decision: actionpolicy.DecisionAllow}
	executor := NewToolExecutor(body.NewRegistry(), body.NewGuard(body.NewRegistry(), &recordingAuthority{}), policy, "doll")
	sender := &communicationSender{
		peers:  []communication.Peer{{PrincipalID: "owner-1", Authority: "owner", ActiveGatewayRoute: true, EligibleSessionCount: 1}},
		result: communication.Result{DeliveryID: "delivery-discovered", Status: sessionevent.DeliveryAccepted},
	}
	executor.SetCommunicationSender(sender)
	discovered := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "discover", Name: "core.communication__list_peers"}, ptrTool(communicationPeersTool()))
	if discovered.Status != inference.ToolResultSuccess || discovered.Result["peer_count"] != 1 {
		t.Fatalf("discovered=%#v", discovered)
	}
	peers, ok := discovered.Result["peers"].([]communication.Peer)
	if !ok || len(peers) != 1 || peers[0].PrincipalID != "owner-1" {
		t.Fatalf("peers=%#v", discovered.Result["peers"])
	}
	sent := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "send", Name: "core.communication__send_text", Arguments: map[string]any{
		"recipient_principal_id": peers[0].PrincipalID, "text": "hello after discovery",
	}}, ptrTool(communicationTool()))
	if sent.Status != inference.ToolResultSuccess || len(sender.requests) != 1 || sender.requests[0].RecipientPrincipalID != "owner-1" {
		t.Fatalf("sent=%#v requests=%#v", sent, sender.requests)
	}
}

func TestApprovedCommunicationUsesOriginalRecipientAndDoesNotBodyRoute(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	executor := NewToolExecutor(body.NewRegistry(), body.NewGuard(body.NewRegistry(), &recordingAuthority{}), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	sender := &communicationSender{result: communication.Result{DeliveryID: "delivery-approved", Status: sessionevent.DeliveryAccepted}}
	executor.SetCommunicationSender(sender)
	call := inference.ToolCall{ID: "approval-send", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "peer-1", "text": "approved text"}}
	result := executor.ExecuteCorrelatedCall(context.Background(), cognitiveexecution.ToolCorrelation{IntentionID: "intent-approval"}, call, ptrTool(communicationTool()))
	if result.Error == nil || result.Error.PermissionRequestID == "" || len(sender.requests) != 0 {
		t.Fatalf("result=%#v", result)
	}
	approved, err := requests.Approve(context.Background(), permissionrequest.ID(result.Error.PermissionRequestID), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.HandlePermissionResolution(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if len(sender.requests) != 1 || sender.requests[0].RecipientPrincipalID != "peer-1" || sender.requests[0].Text != "approved text" {
		t.Fatalf("requests=%#v", sender.requests)
	}
	if err = executor.HandlePermissionResolution(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if len(sender.requests) != 1 {
		t.Fatalf("approval replay sent %d messages", len(sender.requests))
	}
}

func ptrTool(tool inference.Tool) *inference.Tool { return &tool }

func TestPulseCanInitiateCommunicationThroughSharedExecution(t *testing.T) {
	policy := &permissionPolicy{decision: actionpolicy.DecisionAllow}
	executor := NewToolExecutor(body.NewRegistry(), body.NewGuard(body.NewRegistry(), &recordingAuthority{}), policy, "doll")
	sender := &communicationSender{result: communication.Result{DeliveryID: "pulse-delivery", Status: sessionevent.DeliveryAccepted}}
	executor.SetCommunicationSender(sender)
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"tell the owner","matters":true,"reason":"important autonomous observation"}`},
		{ToolCalls: []inference.ToolCall{{ID: "pulse-send", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "owner-1", "text": "I noticed something important."}}}},
		{Content: `{"summary":"owner contacted","proposed_action":"wait","observations":[],"should_reorient":false,"request_future_cognition":false}`},
	}}
	model := testModelConfiguration("communication", inference.DefaultModelContextBudget())
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll"}}},
		WithModelConfiguration(model), WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model),
		WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), WithToolExecutor(executor))
	if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.9, EffectivePressure: 0.8}); err != nil {
		t.Fatal(err)
	}
	if len(sender.requests) != 1 || sender.requests[0].Text != "I noticed something important." || sender.requests[0].Correlation.CognitiveRunID == "" {
		t.Fatalf("requests=%#v", sender.requests)
	}
}

func TestPulseCanDiscoverPeerThenCommunicateThroughSharedExecution(t *testing.T) {
	policy := &permissionPolicy{decision: actionpolicy.DecisionAllow}
	executor := NewToolExecutor(body.NewRegistry(), body.NewGuard(body.NewRegistry(), &recordingAuthority{}), policy, "doll")
	sender := &communicationSender{
		peers:  []communication.Peer{{PrincipalID: "owner-1", Authority: "owner", ActiveGatewayRoute: true}},
		result: communication.Result{DeliveryID: "pulse-discovered-delivery", Status: sessionevent.DeliveryAccepted},
	}
	executor.SetCommunicationSender(sender)
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"contact someone","matters":true,"reason":"important observation"}`},
		{ToolCalls: []inference.ToolCall{{ID: "discover-peer", Name: "core.communication__list_peers", Arguments: map[string]any{}}}},
		{ToolCalls: []inference.ToolCall{{ID: "send-peer", Name: "core.communication__send_text", Arguments: map[string]any{"recipient_principal_id": "owner-1", "text": "An autonomous update."}}}},
		{Content: `{"summary":"peer contacted","proposed_action":"wait","observations":[],"should_reorient":false,"request_future_cognition":false}`},
	}}
	model := testModelConfiguration("communication-discovery", inference.DefaultModelContextBudget())
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll"}}},
		WithModelConfiguration(model), WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model),
		WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), WithToolExecutor(executor))
	if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.9, EffectivePressure: 0.8}); err != nil {
		t.Fatal(err)
	}
	if len(sender.requests) != 1 || sender.requests[0].RecipientPrincipalID != "owner-1" || sender.requests[0].Correlation.CognitiveRunID == "" {
		t.Fatalf("requests=%#v", sender.requests)
	}
}
