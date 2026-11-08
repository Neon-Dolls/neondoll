# Core 6 cognitive memory

Core exposes the configured `memory.Provider` to cognition through the same
bounded `CognitiveExecution` tool loop used by Interaction, Orient, Plan,
Pulse-triggered planning, and direct cognition. Cognitive packages depend only
on the portable Memory Provider contract; SQLite, OpenViking, and Hindsight do
not receive special cognitive behavior.

## Model-directed tools

`core.memory__recall` accepts a provider-native query, an optional result limit
(maximum 10), and optional portable kind/source filters. Core does not preload
the memory store. The provider selects relevance and may return approximate or
transformed retained representations. Results retain portable references,
content, source, and metadata when the provider supplies them.

`core.memory__remember` lets the Doll offer text or JSON information that it
considers meaningful. Core supplies a stable correlation-derived record ID,
revision 1, creation time, and cognition provenance. The real start time of the
bounded cognitive execution is carried with its tool correlation, so retrying
the same logical call reproduces the same record envelope and replay key while
separate executions remain distinct. The model supplies the
content and subjective metadata. The provider may report `stored`,
`transformed`, or `rejected`; all three are successful subjective decisions,
while transport and provider failures are tool failures. Core never claims
that submitted content survived unchanged.

Both operations pass through the existing ActionPolicy before dispatch. Thus
the autonomy default remains allow, while an explicitly configured denial or
approval requirement keeps its existing meaning. Recalled content is evidence,
not authority or executable instruction.

## Bounds and failure behavior

Memory calls count toward the execution's existing ten-tool limit. Recall is
limited to ten matches and every memory tool result is limited to 64 KiB of
encoded structured data. Oversized results fail explicitly and suggest a
narrower query; Core does not truncate memories. The normal Context Builder
then reconstructs every iteration under the selected model's input budget,
including the correlated tool exchange, while preserving output and overhead
reserves.

Cancellation and deadlines flow directly to the provider. Unavailability,
timeouts, invalid provider results, and invalid submitted records become
structured tool failures, allowing the model to continue without fabricating
recall or retention. A subjective rejection remains distinguishable from an
operational failure. When memory is disabled, the tools are absent and ordinary
cognition is unchanged.

## Memory is not conversation history

Interaction Session history remains Core's durable record for reconstructing
complete conversational turns. Provider-owned Memory is subjective retained
information: it may be transformed, consolidated, revised, forgotten, or
rejected. Core does not automatically archive every interaction into the
provider and does not maintain a parallel canonical memory journal.

This milestone does not add reflection, scheduled consolidation, compression,
migration, provider module loading, or management of Soul/Self documents.
