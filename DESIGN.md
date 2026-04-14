# lcdata — Design Document

*Lieutenant Commander Data — Agentic LLM Execution Engine*

---

## What It Is

A declarative agentic execution engine that runs as a server. Each **node** is a directory containing a JSON config file. Nodes compose into **pipelines** with typed data flow, conditional branching, parallel execution, loops, and fan-out/fan-in. The server exposes a REST + WebSocket API so remote services can discover available nodes and execute them.

Same spirit as the rest of the stack: single JSON config, single binary, Go + Chi + JWT, file-first not code-first.

**File-first** means: adding new agents/tasks never requires editing Go source code. You drop a new directory into `nodes/`, write a JSON config and an optional prompt `.md` file. The binary is the engine; `nodes/` is the content. Same as how crusher2 jobs work — you never edit the crusher2 binary to add a new job.

---

## Node Types

Every directory under `nodes/` is a node. The `type` field determines its behavior:

| Type | Description |
|---|---|
| `llm` | LLM call — Anthropic Claude, Ollama, or OpenAI-compatible |
| `stt` | Speech-to-text — Whisper, Deepgram |
| `tts` | Text-to-speech — ElevenLabs, OpenAI |
| `command` | Shell command execution, streams stdout line by line |
| `database` | SQL query — Postgres, MySQL, SQLite |
| `http` | Outbound HTTP request with templated body/headers |
| `transform` | Template-based data reshaping, no external call |
| `pipeline` | Orchestrates other nodes — sequential, conditional, parallel, loop, map |

---

## Node Config Format

Each node lives in `nodes/{name}/{name}.json`. Optional `system.md` for LLM nodes.

### LLM Node

```json
{
  "name": "llm_chat",
  "description": "General-purpose chat agent using Claude",
  "type": "llm",
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "system_prompt_file": "system.md",
  "temperature": 0.7,
  "max_tokens": 4096,
  "stream": true,
  "tools": ["web_search"],
  "structured_output": {
    "type": "object",
    "properties": {
      "response": { "type": "string" },
      "intent":   { "type": "string" }
    }
  },
  "input": {
    "message": { "type": "string",    "required": true },
    "history":  { "type": "[]message","required": false }
  },
  "output": {
    "response": { "type": "string" },
    "usage":    { "type": "object" }
  }
}
```

LLM providers: `anthropic`, `ollama`, `openai`

### STT Node

```json
{
  "name": "transcribe",
  "type": "stt",
  "provider": "whisper",
  "model": "whisper-large-v3",
  "language": "en",
  "input": {
    "audio_url": { "type": "string", "required": true }
  },
  "output": {
    "transcript":  { "type": "string" },
    "confidence":  { "type": "number" },
    "language":    { "type": "string" }
  }
}
```

STT providers: `whisper`, `deepgram`

### TTS Node

```json
{
  "name": "speak_response",
  "type": "tts",
  "provider": "elevenlabs",
  "voice_id": "21m00Tcm4TlvDq8ikWAM",
  "model": "eleven_multilingual_v2",
  "input": {
    "text": { "type": "string", "required": true }
  },
  "output": {
    "audio_url":  { "type": "string" },
    "duration_s": { "type": "number" }
  }
}
```

TTS providers: `elevenlabs`, `openai`

### Command Node

```json
{
  "name": "run_deploy",
  "type": "command",
  "command": "bash",
  "args": ["scripts/deploy.sh"],
  "timeout": "10m",
  "env": {
    "TARGET":  "{{.input.target}}",
    "DRY_RUN": "{{.input.dry_run}}"
  },
  "input": {
    "target":  { "type": "string", "required": true },
    "dry_run": { "type": "string", "default": "false" }
  },
  "output": {
    "stdout":    { "type": "string" },
    "exit_code": { "type": "int" }
  }
}
```

### Database Node

```json
{
  "name": "get_user",
  "type": "database",
  "connection": "main",
  "driver": "postgres",
  "query": "SELECT id, name, email FROM users WHERE id = $1",
  "params": ["{{.input.user_id}}"],
  "input": {
    "user_id": { "type": "string", "required": true }
  },
  "output": {
    "rows":  { "type": "[]object" },
    "count": { "type": "int" }
  }
}
```

### HTTP Node

```json
{
  "name": "post_webhook",
  "type": "http",
  "method": "POST",
  "url": "{{.input.webhook_url}}",
  "headers": {
    "Content-Type": "application/json",
    "Authorization": "Bearer {{.input.token}}"
  },
  "body": "{\"message\": \"{{.input.message}}\"}",
  "input": {
    "webhook_url": { "type": "string", "required": true },
    "token":       { "type": "string", "required": true },
    "message":     { "type": "string", "required": true }
  },
  "output": {
    "status":   { "type": "int" },
    "body":     { "type": "string" }
  }
}
```

### Transform Node

```json
{
  "name": "format_response",
  "type": "transform",
  "template": "{\"summary\": \"{{.input.text}}\", \"word_count\": {{.input.count}}}",
  "input": {
    "text":  { "type": "string", "required": true },
    "count": { "type": "int",    "required": true }
  },
  "output": {
    "result": { "type": "string" }
  }
}
```

---

## Pipelines

Pipelines wire node outputs to inputs using Go templates (`{{.step_id.field}}`). The template syntax is the same mental model as stencil2. Steps execute in order; each step's outputs land in the run context under its `id` namespace.

### Step Types

A pipeline `steps` array can contain any of:

#### Simple step
```json
{
  "id": "stt",
  "node": "transcribe",
  "input": { "audio_url": "{{.input.audio_url}}" }
}
```

#### Switch (conditional)
```json
{
  "id": "dispatch",
  "switch": "{{.classify.intent}}",
  "cases": {
    "search":   { "node": "search_agent",   "input": { "query":   "{{.input.message}}" } },
    "database": { "node": "db_query_agent", "input": { "request": "{{.input.message}}" } },
    "default":  { "node": "llm_chat",       "input": { "message": "{{.input.message}}" } }
  }
}
```

`default` is the fallback case. The switch value is a rendered template — whatever the previous step output.

#### Parallel
```json
{
  "id": "gather",
  "parallel": [
    { "id": "web",  "node": "web_search", "input": { "query": "{{.input.topic}}" } },
    { "id": "docs", "node": "db_lookup",  "input": { "term":  "{{.input.topic}}" } }
  ]
}
```

Branches run concurrently. Their outputs are available as `{{.gather.web.results}}` and `{{.gather.docs.rows}}` — namespaced under the parallel step id, then the branch id.

#### Loop
```json
{
  "id": "refine",
  "loop": {
    "max_iterations": 5,
    "until": "{{gt (toFloat .evaluate.score) 0.8}}",
    "steps": [
      {
        "id": "draft",
        "node": "llm_writer",
        "input": { "topic": "{{.input.topic}}", "previous": "{{.draft.text}}" }
      },
      {
        "id": "evaluate",
        "node": "llm_evaluator",
        "input": { "draft": "{{.draft.text}}" }
      }
    ]
  }
}
```

- `until` is a Go template expression evaluated after each iteration — returns `"true"` to break
- `max_iterations` is a hard safety cap (required)
- Each iteration can reference outputs from the previous iteration since they share the run context
- After the loop, the last iteration's step outputs remain in context

#### Map (fan-out over array)
```json
{
  "id": "summarize_all",
  "map": {
    "over": "{{.search.urls}}",
    "as": "url",
    "node": "summarize_page",
    "input": { "url": "{{.url}}" },
    "collect_as": "summaries",
    "concurrency": 3
  }
}
```

- `over` resolves to an array from context
- `as` names the current item within the input templates
- `collect_as` is the context key where the array of results is written
- `concurrency` controls parallel execution (default `1` = sequential)

---

## Full Pipeline Example

A complete voice assistant: STT → classify intent → branch to handler → TTS

```json
{
  "name": "voice_assistant",
  "type": "pipeline",
  "description": "Voice-in, voice-out assistant with intent routing",
  "steps": [
    {
      "id": "stt",
      "node": "transcribe",
      "input": { "audio_url": "{{.input.audio_url}}" }
    },
    {
      "id": "classify",
      "node": "classify_intent",
      "input": { "text": "{{.stt.transcript}}" }
    },
    {
      "id": "dispatch",
      "switch": "{{.classify.intent}}",
      "cases": {
        "search": {
          "node": "search_agent",
          "input": { "query": "{{.stt.transcript}}" }
        },
        "database": {
          "node": "db_query_agent",
          "input": { "request": "{{.stt.transcript}}" }
        },
        "default": {
          "node": "llm_chat",
          "input": { "message": "{{.stt.transcript}}", "history": "{{.input.history}}" }
        }
      }
    },
    {
      "id": "tts",
      "node": "speak_response",
      "input": { "text": "{{.dispatch.response}}" }
    }
  ],
  "input": {
    "audio_url": { "type": "string",    "required": true },
    "history":   { "type": "[]message", "required": false }
  },
  "output": {
    "audio_url":  "{{.tts.audio_url}}",
    "transcript": "{{.stt.transcript}}",
    "response":   "{{.dispatch.response}}"
  }
}
```

---

## Run Context

All steps in a run share a thread-safe `RunContext` — a namespaced flat map that builds up as steps execute.

```
ctx["input.audio_url"]         = "https://..."       # user-provided
ctx["stt.transcript"]          = "search for X"      # written by stt step
ctx["stt.confidence"]          = 0.97
ctx["classify.intent"]         = "search"            # written by classify step
ctx["dispatch.response"]       = "Here are results"  # written by dispatch step
ctx["tts.audio_url"]           = "https://..."       # written by tts step
```

Steps read via Go templates (`{{.stt.transcript}}`), write by returning their declared output fields. The pipeline's `output` block renders its values from context using the same template syntax, producing the final run result.

---

## Run Context Template Functions

Available in all template strings:

| Function | Description |
|---|---|
| `{{.step.field}}` | Access any step output |
| `{{.input.field}}` | Access run inputs |
| `{{toJSON .value}}` | Marshal value to JSON string |
| `{{fromJSON .str}}` | Parse JSON string to object |
| `{{toFloat .value}}` | Convert to float64 |
| `{{toInt .value}}` | Convert to int |
| `{{gt a b}}` | Greater than |
| `{{lt a b}}` | Less than |
| `{{and a b}}` | Boolean and |
| `{{or a b}}` | Boolean or |
| `{{default val fallback}}` | Use fallback if val is empty |
| `{{join arr sep}}` | Join string array |

---

## Data Flow

```
                    ┌──────────────────────────────┐
  POST /run         │         Runner               │
  { input: {...} }  │                              │
        │           │  1. Resolve node + deps      │
        ▼           │  2. Build RunContext          │
  ┌─────────┐       │  3. Execute steps in order   │
  │  HTTP   │──────►│  4. Each step reads context  │
  │ Handler │       │     via templates            │
  └─────────┘       │  5. Each step writes output  │
        │           │     to context               │
        │           │  6. Render pipeline output   │
        ▼           │     from context templates   │
  ┌─────────┐       └──────────────────────────────┘
  │ Stream  │
  │ Events  │──► WebSocket / SSE clients
  └─────────┘
```

---

## Service API

### Discovery

Remote services call these to understand what's available before calling anything.

```
GET  /api/nodes              → list all nodes with descriptions + I/O schemas
GET  /api/nodes/{name}       → full node spec including resolved pipeline steps
GET  /api/info               → server version, node type list, capabilities
GET  /api/health             → health check
```

### Execution

```
POST /api/nodes/{name}/run       → synchronous, returns full result when complete
POST /api/nodes/{name}/stream    → Server-Sent Events, streams events as they happen
GET  /ws/nodes/{name}            → WebSocket, bidirectional streaming
```

### Run Management

```
GET  /api/runs              → list recent runs
GET  /api/runs/{id}         → get run status and result
POST /api/runs/{id}/cancel  → cancel an in-progress run
```

### Run Request Body

```json
{
  "input": {
    "audio_url": "https://example.com/audio.mp3",
    "history":   []
  },
  "run_id": "optional-client-provided-id",
  "env":    "default"
}
```

### Run Response

```json
{
  "run_id":      "abc123",
  "node":        "voice_assistant",
  "status":      "completed",
  "output": {
    "audio_url":  "https://...",
    "transcript": "search for X",
    "response":   "Here are results for X"
  },
  "steps": [
    { "id": "stt",      "node": "transcribe",      "status": "completed", "duration_ms": 450 },
    { "id": "classify", "node": "classify_intent",  "status": "completed", "duration_ms": 210 },
    { "id": "dispatch", "node": "search_agent",     "status": "completed", "duration_ms": 1100 },
    { "id": "tts",      "node": "speak_response",   "status": "completed", "duration_ms": 320 }
  ],
  "started_at":  "2026-04-13T21:00:00Z",
  "ended_at":    "2026-04-13T21:00:02.08Z",
  "duration_ms": 2080
}
```

### WebSocket / SSE Events

All streaming connections receive the same event stream:

```json
{ "event": "run_started",    "run_id": "abc", "node": "voice_assistant" }
{ "event": "step_started",   "run_id": "abc", "step_id": "stt",      "node": "transcribe" }
{ "event": "step_completed", "run_id": "abc", "step_id": "stt",      "output": { "transcript": "..." }, "duration_ms": 450 }
{ "event": "step_started",   "run_id": "abc", "step_id": "classify",  "node": "classify_intent" }
{ "event": "step_completed", "run_id": "abc", "step_id": "classify",  "output": { "intent": "search" } }
{ "event": "chunk",          "run_id": "abc", "step_id": "dispatch",  "data": "Here are " }
{ "event": "chunk",          "run_id": "abc", "step_id": "dispatch",  "data": "results for X" }
{ "event": "step_completed", "run_id": "abc", "step_id": "dispatch",  "output": { "response": "..." } }
{ "event": "step_started",   "run_id": "abc", "step_id": "tts",       "node": "speak_response" }
{ "event": "step_completed", "run_id": "abc", "step_id": "tts",       "output": { "audio_url": "..." } }
{ "event": "run_completed",  "run_id": "abc", "output": { ... }, "duration_ms": 2080 }
```

Event types: `run_started`, `run_completed`, `run_failed`, `run_cancelled`, `step_started`, `step_completed`, `step_failed`, `chunk`, `loop_iteration`, `map_progress`

Every node type supports streaming — LLM tokens as `chunk` events, command stdout lines as `chunk` events, TTS audio as `chunk` events, DB rows as `chunk` events.

---

## Credentials Config (`~/lcdataenv.json`)

Lookup order: `~/lcdataenv.json` → `./nodes/env.json` (same pattern as crusher2)

```json
{
  "environments": {
    "default": {
      "anthropicKey":   "sk-ant-...",
      "ollamaEndpoint": "http://localhost:11434",
      "openaiKey":      "sk-...",
      "elevenlabsKey":  "...",
      "deepgramKey":    "...",
      "dbConnections": {
        "main":     "postgres://user:pass@host:5432/db",
        "readonly": "postgres://user:pass@host:5432/db?sslmode=require"
      }
    },
    "production": {
      "anthropicKey": "sk-ant-...",
      "dbConnections": {
        "main": "postgres://..."
      }
    }
  }
}
```

---

## Server Config (`lcdata.json`)

```json
{
  "port":                8080,
  "jwt_secret":          "change-this-in-production",
  "require_jwt":         true,
  "nodes_path":          "./nodes",
  "env":                 "default",
  "log_level":           "info",
  "max_concurrent_runs": 10,
  "run_timeout":         "5m",
  "run_history":         100
}
```

---

## CLI Commands

```
lcdata serve                                   start the HTTP + WebSocket server
lcdata list                                    list all nodes in nodes/ folder
lcdata show [name]                             show node config + resolved pipeline steps
lcdata run [name] --input key=val --env prod   run a node locally (no server needed)
lcdata validate                                validate all node configs
lcdata graph [name]                            print dependency/flow tree
lcdata generate-jwt --client my-service        generate a JWT token for a client
lcdata version                                 show version
```

---

## Project Structure

```
lcdata/
  main.go
  go.mod
  DESIGN.md
  lcdata.json.example
  lcdataenv.json.example
  cmd/
    root.go
    serve.go
    list.go
    show.go
    run.go
    validate.go
    graph.go
    jwt.go
    version.go
  internal/
    lcdata/
      config.go         server config (lcdata.json)
      environment.go    credentials config (lcdataenv.json)
      node.go           Node struct, JSON loading, discovery
      pipeline.go       Step type definitions
      runner.go         execution engine, topological sort
      context.go        RunContext, template rendering
      stream.go         WebSocket + SSE event system
      flow.go           switch / parallel / loop / map handlers
      executor.go       per-type execution dispatch
      executor_llm.go   LLM execution (Anthropic, Ollama, OpenAI)
      executor_stt.go   speech-to-text execution
      executor_tts.go   text-to-speech execution
      executor_cmd.go   command execution with stdout streaming
      executor_db.go    database query execution
      executor_http.go  outbound HTTP execution
      executor_xfm.go   transform execution
  nodes/
    llm_chat/
      llm_chat.json
      system.md
    classify_intent/
      classify_intent.json
      system.md
    voice_assistant/
      voice_assistant.json
```

---

## What Makes It Distinct From Other Frameworks

| | lcdata | LangChain/LangGraph | n8n |
|---|---|---|---|
| Config format | JSON files | Python code | Visual UI / JSON |
| Add new agent | Drop a folder | Write Python class | Drag nodes |
| Streaming | All node types, unified WS events | Per-chain, varies | Limited |
| Self-describing API | `/api/nodes` live registry | No | Workflow export |
| Flow control | switch/parallel/loop/map in JSON | Graph edges in code | Node connections |
| Provider switch | Change one JSON field | Change class import | Reconfigure credential |
| Deploy | Single Go binary | Python env + deps | Node.js app |
| CLI mode | `lcdata run name` | Script the chain | No |
| Spirit | crusher2 jobs but for LLMs | Frameworks | SaaS tool |
