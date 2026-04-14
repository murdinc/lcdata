# lcdata

**Lieutenant Commander Data** — A declarative agentic LLM execution engine.

Drop a folder into `nodes/`, write a JSON config, and the engine exposes it as a REST + WebSocket API endpoint. No code changes. No recompile. Nodes compose into pipelines with conditional branching, parallel execution, loops, and fan-out — all in JSON.

Same spirit as the rest of the stack: single Go binary, JSON config files, Chi router, JWT auth, Cobra CLI.

---

## Quick Start

```bash
# Build
go build -o lcdata .

# First run creates lcdata.json with defaults
./lcdata serve

# List available nodes
./lcdata list

# Run a node from the CLI
./lcdata run llm_chat --input message="Hello"

# Validate all node configs
./lcdata validate

# Show the execution graph for a pipeline
./lcdata graph smart_assistant
```

---

## The Core Idea

Every node is a directory:

```
nodes/
  my_agent/
    my_agent.json     ← node config
    system.md         ← optional system prompt (LLM nodes)
```

The `type` field determines how it runs. Nodes can be wired together into pipelines using Go template expressions (`{{.step_id.field}}`). Adding a new agent means dropping a folder — no Go code required.

---

## Node Types

| Type | Description |
|------|-------------|
| `llm` | LLM call — Anthropic Claude, Ollama, or OpenAI-compatible |
| `http` | Outbound HTTP request with templated URL, headers, and body |
| `search` | Web search — Brave API or SearXNG |
| `file` | File operations — read, write, append, exists, delete, list |
| `command` | Shell command with streaming stdout |
| `transform` | Template-based data reshaping, no external call |
| `database` | SQL query — Postgres, MySQL, SQLite *(stubbed)* |
| `stt` | Speech-to-text — Deepgram *(stubbed)* |
| `tts` | Text-to-speech — ElevenLabs *(stubbed)* |
| `pipeline` | Orchestrates other nodes — sequential, switch, parallel, loop, map |

---

## Node Config Reference

### LLM Node

```json
{
  "name": "llm_chat",
  "description": "General-purpose chat using Claude",
  "type": "llm",
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "system_prompt_file": "system.md",
  "temperature": 0.7,
  "max_tokens": 4096,
  "stream": true,
  "structured_output": {
    "intent": { "type": "string" },
    "confidence": { "type": "number" }
  },
  "input": {
    "message": { "type": "string", "required": true },
    "history":  { "type": "array",  "required": false }
  },
  "output": {
    "response": { "type": "string" },
    "usage":    { "type": "object" }
  }
}
```

- **providers:** `anthropic`, `ollama`, `openai`
- **`stream: true`** emits `chunk` events over WebSocket/SSE as tokens arrive
- **`structured_output`** — when set, the LLM response is parsed as JSON and each field is merged into the output map alongside `response`
- **`history`** input — pass an array of `{role, content}` objects for multi-turn conversations

### HTTP Node

```json
{
  "name": "fetch_url",
  "type": "http",
  "method": "GET",
  "url": "{{.input.url}}",
  "strip_html": true,
  "headers": {
    "Authorization": "Bearer {{.input.token}}"
  },
  "body": "{\"query\": \"{{.input.q}}\"}",
  "input": {
    "url": { "type": "string", "required": true }
  },
  "output": {
    "status": { "type": "number" },
    "body":   { "type": "string" }
  }
}
```

- **`strip_html: true`** strips tags, scripts, and styles — returns clean plain text
- URL, headers, and body are all Go templates rendered against the run context

### Search Node

```json
{
  "name": "web_search",
  "type": "search",
  "search_provider": "brave",
  "search_count": 10,
  "input": {
    "query": { "type": "string", "required": true }
  },
  "output": {
    "results": { "type": "array" },
    "count":   { "type": "number" }
  }
}
```

- **providers:** `brave`, `searxng`
- Returns `results` as an array of `{title, url, description}` objects

### File Node

```json
{
  "name": "read_file",
  "type": "file",
  "operation": "read",
  "input": {
    "path": { "type": "string", "required": true }
  },
  "output": {
    "content": { "type": "string" },
    "size":    { "type": "number" }
  }
}
```

- **operations:** `read`, `write`, `append`, `exists`, `delete`, `list`
- `write` and `append` require `input.content`
- Parent directories are created automatically on `write`/`append`

### Command Node

```json
{
  "name": "run_script",
  "type": "command",
  "command": "bash",
  "args": ["scripts/process.sh"],
  "timeout": "10m",
  "env": {
    "TARGET": "{{.input.target}}"
  },
  "input": {
    "target": { "type": "string", "required": true }
  },
  "output": {
    "stdout":    { "type": "string" },
    "exit_code": { "type": "number" }
  }
}
```

- Stdout is streamed line-by-line as `chunk` events over WebSocket/SSE
- `timeout` accepts Go duration strings: `30s`, `5m`, `1h`

### Transform Node

```json
{
  "name": "format_report",
  "type": "transform",
  "template": "# Report\n\n{{.input.title}}\n\n{{.input.body}}",
  "input": {
    "title": { "type": "string", "required": true },
    "body":  { "type": "string", "required": true }
  },
  "output": {
    "result": { "type": "string" }
  }
}
```

---

## Pipelines

Pipelines wire nodes together. Each step's output is available to all subsequent steps via `{{.step_id.field}}`. The pipeline's `output` block defines what gets returned to the caller.

```json
{
  "name": "my_pipeline",
  "type": "pipeline",
  "steps": [ ... ],
  "input": { ... },
  "output": {
    "answer": "{{.final_step.response}}",
    "items":  "{{.gather.results}}"
  }
}
```

### Sequential Step

```json
{
  "id": "summarize",
  "node": "summarize",
  "input": {
    "message": "{{.fetch.body}}"
  }
}
```

### Switch (Conditional Branching)

Routes to a different node based on a runtime value. LLM outputs like `{"intent": "search"}` are automatically normalized to `"search"`.

```json
{
  "id": "route",
  "switch": "{{.classify.intent}}",
  "cases": {
    "search":   { "node": "web_search",  "input": { "query":   "{{.input.message}}" } },
    "chat":     { "node": "llm_chat",    "input": { "message": "{{.input.message}}" } },
    "default":  { "node": "llm_chat",    "input": { "message": "{{.input.message}}" } }
  }
}
```

### Parallel

All branches run concurrently. Outputs are namespaced: `{{.gather.web.results}}`, `{{.gather.db.rows}}`.

```json
{
  "id": "gather",
  "parallel": [
    { "id": "web", "node": "web_search", "input": { "query": "{{.input.topic}}" } },
    { "id": "db",  "node": "db_lookup",  "input": { "term":  "{{.input.topic}}" } }
  ]
}
```

### Loop

Repeats inner steps until a condition is true or `max_iterations` is reached. Each iteration shares the same run context so later iterations can reference earlier ones.

```json
{
  "id": "refine",
  "loop": {
    "max_iterations": 5,
    "until": "{{gt (toFloat .evaluate.score) 0.8}}",
    "steps": [
      { "id": "draft",    "node": "llm_writer",    "input": { "topic": "{{.input.topic}}" } },
      { "id": "evaluate", "node": "llm_evaluator",  "input": { "draft": "{{.draft.text}}" } }
    ]
  }
}
```

### Map (Fan-out)

Runs a node once per item in an array. Results are collected into a new context key.

```json
{
  "id": "fetch_all",
  "map": {
    "over":        "{{.search.results}}",
    "as":          "result",
    "node":        "fetch_url",
    "concurrency": 3,
    "input": {
      "url": "{{.result.url}}"
    },
    "collect_as": "pages"
  }
}
```

- `as` names the current item for use in `input` templates
- `collect_as` sets the context key where the result array lands
- `concurrency` controls how many items run in parallel (default: 1 = sequential)

---

## Run Context

All steps in a run share a thread-safe `RunContext`. Steps read via templates and write by returning their output fields. Keys are namespaced by step ID.

```
input.message          → user-provided input
classify.intent        → written by the "classify" step
gather.web.results     → written by branch "web" inside parallel step "gather"
fetch_all.0            → written by map step "fetch_all" for item 0
pages                  → the collected array from a map step's collect_as
```

### Template Functions

| Function | Usage |
|----------|-------|
| `{{.step.field}}` | Access any step output |
| `{{.input.field}}` | Access run inputs |
| `{{toJSON .value}}` | Marshal to JSON string |
| `{{fromJSON .str}}` | Parse JSON string |
| `{{toFloat .value}}` | Convert to float64 |
| `{{toInt .value}}` | Convert to int |
| `{{default val fallback}}` | Use fallback if val is empty/nil |
| `{{join arr sep}}` | Join string array with separator |
| `{{gt a b}}` / `{{lt a b}}` | Comparison (for loop conditions) |

Simple path references like `{{.step.field}}` preserve the original type (array, map, number). Complex templates like `"https://{{.host}}/{{.path}}"` produce strings.

---

## Built-in Nodes

The `nodes/` directory ships with these ready-to-use nodes:

| Node | Type | What it does |
|------|------|-------------|
| `llm_chat` | llm | Streaming chat with Claude (sonnet-4-6) |
| `classify_intent` | llm | Classifies input as: search, database, chat, command |
| `classify_sentiment` | llm | Returns `{sentiment, confidence, explanation}` |
| `extract_entities` | llm | Returns `{people, organizations, locations, dates, topics}` |
| `summarize` | llm | Condenses text to 2-4 sentences |
| `translate` | llm | Translates to any language |
| `fetch_url` | http | Fetches a URL, strips HTML to plain text |
| `web_search` | search | Brave web search → `[{title, url, description}]` |
| `read_file` | file | Reads a file from disk |
| `write_file` | file | Writes a file to disk |
| `research_pipeline` | pipeline | web_search → fetch pages → summarize each → synthesize answer |
| `smart_assistant` | pipeline | Classify intent → route to research, translate, or chat |
| `analyze_document` | pipeline | Read file → parallel analysis → compose report → write file |

### Pipeline: `research_pipeline`

```
web_search
  └── map: fetch_url (concurrency: 3)
        └── map: summarize (concurrency: 3)
              └── llm_chat (synthesize)
```

Returns `{response, search_results, summaries}`.

### Pipeline: `smart_assistant`

```
classify_intent
  └── switch on intent:
        "search"   → research_pipeline
        "translate" → translate
        "default"  → llm_chat
```

Returns `{response, intent}`.

### Pipeline: `analyze_document`

```
read_file
  └── parallel:
        ├── summarize
        ├── extract_entities
        └── classify_sentiment
              └── llm_chat (compose report)
                    └── write_file
```

Returns `{report, output_path, summary, sentiment}`.

---

## API

### Discovery

```
GET /api/nodes           → list all nodes with descriptions and I/O schemas
GET /api/nodes/{name}    → full node spec
GET /api/health          → health check
GET /api/info            → server version and capabilities
```

### Execution

```
POST /api/nodes/{name}/run     → synchronous, waits for full result
POST /api/nodes/{name}/stream  → Server-Sent Events, streams as it runs
GET  /ws/nodes/{name}          → WebSocket, bidirectional streaming
```

**Request body:**
```json
{
  "input": { "message": "What is the capital of France?" },
  "env":    "default"
}
```

**Response:**
```json
{
  "run_id":      "a3f9b2c1",
  "node":        "smart_assistant",
  "status":      "completed",
  "output": {
    "response": "The capital of France is Paris.",
    "intent":   "chat"
  },
  "steps": [
    { "id": "classify", "node": "classify_intent", "status": "completed", "duration_ms": 180 },
    { "id": "handle",   "node": "llm_chat",         "status": "completed", "duration_ms": 620 }
  ],
  "duration_ms": 800
}
```

### Run Management

```
GET  /api/runs           → list recent runs
GET  /api/runs/{id}      → get run status and full result
POST /api/runs/{id}/cancel → cancel an in-progress run
```

### Streaming Events

All streaming connections (WebSocket and SSE) receive the same event stream:

```json
{"event":"run_started",    "run_id":"abc", "node":"smart_assistant"}
{"event":"step_started",   "run_id":"abc", "step_id":"classify",  "node":"classify_intent"}
{"event":"step_completed", "run_id":"abc", "step_id":"classify",  "output":{"intent":"search"}, "duration_ms":180}
{"event":"step_started",   "run_id":"abc", "step_id":"handle",    "node":"research_pipeline"}
{"event":"chunk",          "run_id":"abc", "step_id":"synthesize","data":"Based on the search results..."}
{"event":"map_progress",   "run_id":"abc", "step_id":"fetch_all", "progress":3, "total":10}
{"event":"step_completed", "run_id":"abc", "step_id":"handle",    "output":{...}, "duration_ms":4200}
{"event":"run_completed",  "run_id":"abc", "output":{...}, "duration_ms":4380}
```

**Event types:** `run_started`, `run_completed`, `run_failed`, `run_cancelled`, `step_started`, `step_completed`, `step_failed`, `chunk`, `loop_iteration`, `map_progress`

---

## Configuration

### Server Config (`lcdata.json`)

Created automatically on first run.

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

### Credentials (`~/lcdataenv.json`)

Lookup order: `~/lcdataenv.json` → `./nodes/env.json`. All fields also fall back to environment variables.

```json
{
  "environments": {
    "default": {
      "anthropicKey":    "sk-ant-...",
      "ollamaEndpoint":  "http://localhost:11434",
      "openaiKey":       "sk-...",
      "braveKey":        "BSA...",
      "searxngEndpoint": "http://localhost:8888",
      "elevenlabsKey":   "",
      "deepgramKey":     "",
      "dbConnections": {
        "main": "postgres://user:pass@localhost:5432/mydb"
      }
    },
    "production": {
      "anthropicKey": "sk-ant-...",
      "dbConnections": {
        "main": "postgres://user:pass@prod:5432/mydb?sslmode=require"
      }
    }
  }
}
```

**Environment variable fallbacks:**

| Config key | Env var |
|-----------|---------|
| `anthropicKey` | `ANTHROPIC_API_KEY` |
| `openaiKey` | `OPENAI_API_KEY` |
| `ollamaEndpoint` | `OLLAMA_ENDPOINT` |
| `braveKey` | `BRAVE_API_KEY` |
| `searxngEndpoint` | `SEARXNG_ENDPOINT` |
| `elevenlabsKey` | `ELEVENLABS_API_KEY` |
| `deepgramKey` | `DEEPGRAM_API_KEY` |

---

## CLI

```
lcdata serve                                    start the HTTP + WebSocket server
lcdata list                                     list all nodes
lcdata show [name]                              show full node config
lcdata run [name] --input key=val --env prod    run a node locally (no server)
lcdata validate                                 validate all node configs
lcdata graph [name]                             print execution tree with icons
lcdata generate-jwt --client my-service         generate a signed JWT
lcdata version                                  show version
```

**Graph output example:**
```
◼ analyze_document  (pipeline)
├── [read]
│   ▤ read_file  (file)
├── [analyze] parallel (3 branches)
│   ├── branch "summary"
│   │   ◆ summarize  (llm)
│   ├── branch "entities"
│   │   ◆ extract_entities  (llm)
│   └── branch "sentiment"
│       ◆ classify_sentiment  (llm)
├── [compose]
│   ◆ llm_chat  (llm)
└── [write]
    ▤ write_file  (file)
```

**Node type icons:** ◆ llm · ◈ http · ⊕ search · ▤ file · ▶ command · ▣ database · ◇ transform · ◼ pipeline · ◎ stt · ◉ tts

---

## Auth

JWT authentication is enabled by default. Disable with `"require_jwt": false` in `lcdata.json`.

Generate a token:
```bash
lcdata generate-jwt --client my-service
```

Use in requests:
```
Authorization: Bearer <token>
```

---

## Project Structure

```
lcdata/
  main.go
  go.mod
  lcdata.json.example
  lcdataenv.json.example
  DESIGN.md
  cmd/
    root.go           Cobra root + global flags
    serve.go          HTTP server, WebSocket, SSE, JWT middleware
    list.go           lcdata list
    show.go           lcdata show
    run.go            lcdata run
    validate.go       lcdata validate
    graph.go          lcdata graph (ASCII tree)
    jwt.go            lcdata generate-jwt
    version.go        lcdata version
  internal/lcdata/
    config.go         Server config (lcdata.json)
    environment.go    Credentials config (lcdataenv.json)
    node.go           Node struct, JSON loading, field schema
    pipeline.go       Step, SwitchCase, LoopConfig, MapConfig types
    runner.go         Run lifecycle, async execution, history
    context.go        RunContext, template rendering, type preservation
    stream.go         Event types, Run struct, StepResult
    flow.go           Pipeline execution, switch/parallel/loop/map
    executor.go       Per-type dispatch
    executor_llm.go   Anthropic, Ollama, OpenAI (streaming + structured output)
    executor_http.go  HTTP requests + HTML stripping
    executor_search.go Brave API + SearXNG
    executor_file.go  File read/write/append/exists/delete/list
    executor_cmd.go   Command execution with streaming stdout
    executor_xfm.go   Transform execution
    executor_db.go    Database (stubbed)
    executor_stt.go   STT (stubbed)
    executor_tts.go   TTS (stubbed)
  nodes/
    llm_chat/
    classify_intent/
    classify_sentiment/
    extract_entities/
    summarize/
    translate/
    fetch_url/
    web_search/
    read_file/
    write_file/
    research_pipeline/
    smart_assistant/
    analyze_document/
```

---

## Dependencies

```
github.com/anthropics/anthropic-sdk-go  v0.2.0-alpha.4
github.com/go-chi/chi/v5                v5.2.3
github.com/go-chi/cors                  v1.2.2
github.com/golang-jwt/jwt/v5            v5.3.0
github.com/gorilla/websocket            v1.5.3
github.com/spf13/cobra                  v1.8.0
```

Go 1.23+

---

## Comparison

| | lcdata | LangChain | n8n |
|---|---|---|---|
| Config format | JSON files | Python code | Visual UI |
| Add new agent | Drop a folder | Write a class | Drag nodes |
| Streaming | All node types, unified events | Per-chain, varies | Limited |
| Self-describing API | `/api/nodes` live registry | No | Workflow export |
| Flow control | switch/parallel/loop/map in JSON | Graph edges in code | Node connections |
| Provider switch | Change one JSON field | Change class import | Reconfigure credential |
| Deploy | Single Go binary | Python env + deps | Node.js app |
| CLI mode | `lcdata run name` | Script the chain | No |
