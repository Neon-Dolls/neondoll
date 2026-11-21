# Core 6 local Skill lifecycle

Core's first working Skill lifecycle is a native, local/offline catalog. It
uses the standard Agent Skills directory format: every immediate child of the
configured catalog directory is a Skill directory rooted at `SKILL.md`.

## Configuration and startup

```json
{
  "paths": { "data_dir": "data" },
  "skills": {
    "directory": "skills",
    "max_skills": 128,
    "max_instruction_bytes": 262144,
    "max_resources": 16,
    "max_resource_bytes": 524288
  }
}
```

An empty `skills.directory` disables the catalog. A relative directory is
resolved inside `paths.data_dir`; traversal and escaping symlinks are rejected.
An explicitly configured absolute directory is supported. Startup validates
every discovered `SKILL.md` and fails rather than silently hiding malformed,
duplicate, or oversized Skills.

`name` and `description` come from standard YAML frontmatter, and the name must
match its parent directory. Local installations use revision 1 unless the
standard frontmatter `metadata` map contains a positive
`neondoll.revision` value. This initial lifecycle treats the configured
directory as durable installation state; restart reconstructs the catalog from
that directory. In-place updates require a daemon restart and must change the
declared revision. Package acquisition, dependency resolution, and updating
remain later work.

## Progressive disclosure

Core exposes two shared cognitive tools:

- `core.skills__list` returns concise ID, revision, name, description, source,
  relative directory, and activation status. It does not read instructions.
- `core.skills__read` loads one exact active revision and only the explicitly
  requested relative supporting resources.

The normal bounded cognitive tool exchange carries selected instructions into
the next inference iteration. Core does not inject every installed Skill into
every request, and it does not append loaded instructions to Soul, Self, or
Memory. If selected content cannot fit the model's required context budget,
the existing bounded execution path fails explicitly before another inference
call.

## Authority

Skills are operational knowledge, not capability or authority. Frontmatter,
Markdown, scripts, and experimental `allowed-tools` metadata are untrusted
guidance. Skill list/read calls pass through ActionPolicy, and every action
suggested by a Skill still passes independently through ToolExecutor,
ActionPolicy, Body resolution, and Body Guard. Installing or reading a Skill
never registers a capability or grants permission.

The repository example at `examples/skills/runtime-inspection` demonstrates
discovery, selective loading, and use of the existing Local Body
`runtime.info` capability.

## Deferred lifecycle work

This increment does not implement Package Source acquisition, remote catalogs,
dependency installation, activation administration, updates, publishing,
automatic learning, or the production DollCard v2 Skill archive pipeline. The
existing portable Skill contract remains the boundary for that later work.
