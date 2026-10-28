# Autonomous permission escalation

Core evaluates action-wide policy before Body discovery. `allow` enters the
existing Body Resolver and Guard path, `deny` is terminal, and
`require_approval` creates a durable permission request without dispatching to
any Body. Body-local denial, unavailability, and failure remain eligible for
resolver fallback and never become an action-wide prohibition.

The request records the Doll and configured Owner IDs, capability, operation,
arguments, tool-call ID, and ephemeral Cognitive Run ID. The original
Cognitive Run is not persisted. Its tool loop receives a correlated
`approval_required` result containing the permission request ID and may finish
normally while the Owner decides.

An authenticated Owner decision still enters through the existing
PermissionBridge and presented Session. Approval atomically makes the stored
action ready. A continuation claim is persisted before execution; the action
is then re-evaluated against current ActionPolicy and executed through the same
Body Resolver, Guard, and fallback coordinator used by ordinary cognitive tool
calls. Replayed decisions and daemon restart cannot claim the action again.
Denial is also claimed once and never dispatches a Body. The completed outcome
is stored on the permission request and starts a fresh ephemeral cognition run
so the Doll can incorporate the authoritative decision/result into its next
plan.

Production uses `DollState.Owner.OwnerID` as the intended Core Owner principal.
It must match an Owner principal configured for trusted Gateway identity
resolution so PermissionBridge can select an eligible active Owner Session.

## Failure boundary

Core deliberately prefers duplicate prevention over unsafe automatic replay
after an indeterminate process crash. If the process dies after the durable
continuation claim but before its result is stored, the request remains
`running` and is not executed again automatically. Closing that narrow window
requires an execution target with a specified durable idempotency contract;
Core does not invent one or risk repeating a physical action. Ready but
unclaimed approvals are resumed after restart.
