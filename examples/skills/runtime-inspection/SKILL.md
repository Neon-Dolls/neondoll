---
name: runtime-inspection
description: Inspect the Core runtime safely. Use when asked about the current Core version, platform, architecture, or available local capabilities.
metadata:
  neondoll.revision: "1"
allowed-tools: runtime.info__read runtime.info__list
---

# Runtime inspection

Use Core's existing `runtime.info` capability rather than guessing runtime
details.

1. Call `runtime.info__read` for version, operating system, architecture, Go
   version, and CPU count.
2. Call `runtime.info__list` only when the task asks which local capabilities
   are available.
3. Report only the fields relevant to the question.

The `allowed-tools` metadata above is descriptive Skill content. Core's
ActionPolicy and Body Guard remain authoritative for every call.
