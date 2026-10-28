# Intention-to-action continuity

A due Doll intention enters the existing internal wake path. Orient decides
whether it matters, and Plan uses the shared `CognitiveExecution` engine. Plan
therefore has the same bounded multi-step tools, action-wide policy, Body
resolution, Guard authorization, fallback, and permission escalation as other
internal cognition.

The continuity boundary is the durable `DollState.IntentionItem`, not a
persisted Cognitive Run. When a completed plan explicitly requests future
cognition, Core updates the originating intention in place with the new
subject, reason, and wake time and leaves it pending. A later Pulse scan can
rediscover the same intention after restart. If the plan does not request a
future wake, successful reconsideration completes it. Core does not invent a
retry time after failure or deferment.

Tool and permission correlations carry the originating intention ID alongside
the independent Cognitive Run, Session, Turn, and tool-call identities. An
approved action still resumes through the existing durable PermissionRequest
continuation; its outcome starts fresh bounded cognition and includes the
intention correlation. While that permission lifecycle is unresolved, the
originating intention remains pending but is excluded from due-wake discovery,
so Pulse cannot dispatch the protected action repeatedly. This wait condition
is derived from the durable permission record; it is not another intention
state.

After approval or denial, durable outcome notification re-enters the same
intention through a fresh bounded internal-wake cognition run. The completed
action is not repeated, and denial does not itself dispatch an action. The Doll
can complete the intention, explicitly reschedule that same identity, or choose
a different action through the ordinary Plan/tool path. The original Cognitive
Run remains ephemeral.

Progress evidence is durable only when the Doll selects it through the
existing `retain_experience` mechanism. This increment does not add a task
engine, automatic retry policy, or an action ledger. Avoiding an ineffective
repeat remains a cognitive decision based on the preserved intention and
retained Mind state.
