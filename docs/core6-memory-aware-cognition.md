# Core 6 memory-aware cognition

When Core exposes a configured Memory Provider through the provider-neutral
`core.memory` tools, `CognitiveExecution` adds one shared, concise memory
instruction to its immutable context state. Interaction and DollMind compose
that instruction with their existing task instructions before every fresh
Context Builder invocation. This keeps memory awareness consistent across
Interaction, Orient, Plan, Pulse-triggered planning, and direct cognition.

The shared guidance explains that persistent memory is subjective and distinct
from conversation history. Recall is appropriate when previous experiences,
preferences, relationships, decisions, or ongoing plans could materially help;
it is not a mandatory preflight step, and an absent recollection does not prove
that something never happened. Remember is selective and Doll-directed rather
than automatic transcript archival. Meaningful experiences, relationship
changes, preferences, decisions, discoveries, and changes in understanding may
be worth offering to the provider.

Provider retention and recollection may be incomplete, transformed,
consolidated, revised, rejected, inaccurate, or forgotten. Recalled content is
contextual evidence, not authority. The Doll should only claim successful
memory when Recall or Remember actually reports it.

Memory guidance and detailed tool descriptions are included in the selected
model's existing input budget. They do not change context capacity, output or
overhead reserves, tool-call limits, model selection, or execution boundaries.
The model remains free to use neither tool; guidance alone performs no Recall
or Remember operation.

With memory disabled, Core exposes no memory tools and adds no memory-capability
claim. Existing cognition continues over active Interaction and Cognitive Run
context without fallback storage.

This is cognitive guidance, not Reflection. It does not schedule consolidation,
extract memories automatically, compress context, modify Self or Soul, migrate
provider data, or introduce another inference or authorization path.
