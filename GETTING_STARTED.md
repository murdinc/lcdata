# Getting Started — Full Local AI Stack

This guide sets up the complete lcdata personal assistant stack from scratch on a new machine:

- **lcdata** — the pipeline engine (this repo)
- **Ollama** — local LLM inference (no API keys needed)
- **whisper.cpp** — local speech-to-text
- **Piper** — local text-to-speech
- **springg** — local vector database
- **d1** — the voice client CLI

At the end you'll be able to speak to the AI, have it think locally, and hear a response back — no cloud services required.

---

## Prerequisites

### macOS

```bash
# Xcode command line tools (needed for CGO in d1)
xcode-select --install

# Homebrew (if not installed)
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# Go 1.23 or later
brew install go

# Verify
go version   # must be 1.23+
```

### Linux (Debian/Ubuntu)

```bash
sudo apt update
sudo apt install -y git build-essential curl wget

# Go — download from https://go.dev/dl/ and follow install instructions
# or:
sudo snap install go --classic
go version   # must be 1.23+
```

---

## 1. Clone and build lcdata

```bash
git clone https://github.com/murdinc/lcdata
cd lcdata
go build -o lcdata .
./lcdata validate
```

Expected output: `All N nodes valid`

---

## 2. Ollama — local LLM

### Install

```bash
# macOS
brew install ollama

# Linux
curl -fsSL https://ollama.com/install.sh | sh
```

### Start the server

```bash
ollama serve
# Leave this running in a separate terminal, or set it up as a service
```

### Pull models

```bash
# Recommended: llama3.2 (2GB, fast, good quality)
ollama pull llama3.2

# Or pull whichever model you prefer:
# ollama pull openclaw
# ollama pull mistral
# ollama pull qwen2.5
# ollama pull phi4-mini

# Verify
ollama list
```

### Update the voice LLM node to use your model

Edit `nodes/voice_llm/voice_llm.json` and change the `model` field to match what `ollama list` shows:

```bash
# Example: change llama3.2 to openclaw
sed -i '' 's/"model": "llama3.2"/"model": "openclaw"/' nodes/voice_llm/voice_llm.json

# Or edit manually:
# "model": "openclaw"
```

Ollama runs at `http://localhost:11434` by default — lcdata uses this automatically, no config needed.

---

## 3. whisper.cpp — local speech-to-text

### Install

```bash
# macOS (easiest)
brew install whisper-cpp
which whisper-cli   # should print a path

# Linux — build from source:
git clone https://github.com/ggerganov/whisper.cpp /opt/whisper.cpp
cd /opt/whisper.cpp
cmake -B build
cmake --build build --config Release -j$(nproc)
sudo ln -sf /opt/whisper.cpp/build/bin/whisper-cli /usr/local/bin/whisper-cli
```

### Download a model

```bash
mkdir -p ~/whisper-models

# base.en — 142MB, fast, good quality (recommended starting point)
curl -L -o ~/whisper-models/ggml-base.en.bin \
  "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin"

# Other options (bigger = better quality, slower):
# ggml-tiny.en.bin    ~75MB  — fastest, OK quality
# ggml-small.en.bin   ~466MB — better quality
# ggml-medium.en.bin  ~1.5GB — great quality, needs more RAM
```

### Test it

```bash
# Record a short WAV and transcribe it (macOS):
# sox -d -r 16000 -c 1 -b 16 /tmp/test.wav trim 0 3
whisper-cli -m ~/whisper-models/ggml-base.en.bin -f /tmp/test.wav -nt
```

### Tell lcdata where the model is

In `lcdataenv.json` (created in step 6), set:
```json
"whisperCppModel": "/Users/YOUR_NAME/whisper-models/ggml-base.en.bin"
```

### Switch voice_stt to use whisper.cpp

Edit `nodes/voice_stt/voice_stt.json`:
```json
{
  "name": "voice_stt",
  "type": "stt",
  "provider": "whisper-cpp",
  "language": "en",
  ...
}
```

```bash
# Quick sed version:
sed -i '' 's/"provider": "openai"/"provider": "whisper-cpp"/' nodes/voice_stt/voice_stt.json
# Also remove the "model" line since whisper-cpp reads it from env config:
# (or set node.model to the full path directly)
```

---

## 4. Piper — local text-to-speech

### Download the binary

```bash
mkdir -p ~/piper

# macOS Apple Silicon (M1/M2/M3)
curl -L -o /tmp/piper.tar.gz \
  "https://github.com/rhasspy/piper/releases/latest/download/piper_macos_aarch64.tar.gz"

# macOS Intel
# curl -L -o /tmp/piper.tar.gz \
#   "https://github.com/rhasspy/piper/releases/latest/download/piper_macos_x64.tar.gz"

# Linux x86_64
# curl -L -o /tmp/piper.tar.gz \
#   "https://github.com/rhasspy/piper/releases/latest/download/piper_linux_x86_64.tar.gz"

tar -xzf /tmp/piper.tar.gz -C ~/piper
ls ~/piper/piper   # should exist

# Make it accessible system-wide (optional)
sudo ln -sf ~/piper/piper /usr/local/bin/piper
```

### Download a voice model

```bash
mkdir -p ~/piper-voices

# en_US-lessac-medium — good quality English, ~63MB (recommended)
curl -L -o ~/piper-voices/en_US-lessac-medium.onnx \
  "https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx"
curl -L -o ~/piper-voices/en_US-lessac-medium.onnx.json \
  "https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx.json"

# Test it
echo "Hello, this is a test." | piper \
  --model ~/piper-voices/en_US-lessac-medium.onnx \
  --output_file /tmp/piper-test.wav
afplay /tmp/piper-test.wav   # macOS
# aplay /tmp/piper-test.wav  # Linux
```

### Switch voice_tts to use Piper

Edit `nodes/voice_tts/voice_tts.json` — set `provider` and `voice_id`:

```json
{
  "name": "voice_tts",
  "type": "tts",
  "provider": "piper",
  "voice_id": "/Users/YOUR_NAME/piper-voices/en_US-lessac-medium.onnx",
  ...
}
```

```bash
# Quick edit (replace YOUR_NAME):
VOICE_PATH="$HOME/piper-voices/en_US-lessac-medium.onnx"
python3 -c "
import json, sys
with open('nodes/voice_tts/voice_tts.json') as f: d = json.load(f)
d['provider'] = 'piper'
d['voice_id'] = '$VOICE_PATH'
del d['model']
print(json.dumps(d, indent=2))
" > /tmp/voice_tts.json && mv /tmp/voice_tts.json nodes/voice_tts/voice_tts.json
```

Also set the `piperBin` path in `lcdataenv.json` (step 6) if piper is not in your PATH:
```json
"piperBin": "/Users/YOUR_NAME/piper/piper"
```

---

## 5. springg — local vector database

springg is a separate Go service that provides vector storage with cosine similarity search, WAL persistence, and optional S3 backup.

```bash
# Clone alongside lcdata (one level up, or anywhere you prefer)
cd ..
git clone https://github.com/murdinc/springg
cd springg
go build -o springg .

# Start springg (default port 8181, no auth)
./springg serve
```

Leave springg running in its own terminal, or set it up as a service.

### Tell lcdata where springg is

In `lcdataenv.json` (step 6), set:
```json
"springgEndpoint": "http://localhost:8181"
```

If you enable JWT auth in springg, also set:
```json
"springgKey": "your-springg-jwt-token"
```

---

## 6. Configure lcdata

### lcdataenv.json

Create `~/lcdataenv.json` (lcdata checks the home directory first):

```json
{
  "environments": {
    "default": {
      "ollamaEndpoint": "http://localhost:11434",
      "springgEndpoint": "http://localhost:8181",

      "whisperCppBin":   "whisper-cli",
      "whisperCppModel": "/Users/YOUR_NAME/whisper-models/ggml-base.en.bin",
      "piperBin":        "piper",

      "dbConnections": {}
    }
  }
}
```

Replace `YOUR_NAME` with your actual username (`echo $USER`).

**One-liner to create it:**
```bash
cat > ~/lcdataenv.json << EOF
{
  "environments": {
    "default": {
      "ollamaEndpoint": "http://localhost:11434",
      "springgEndpoint": "http://localhost:8181",
      "whisperCppBin":   "whisper-cli",
      "whisperCppModel": "$HOME/whisper-models/ggml-base.en.bin",
      "piperBin":        "piper",
      "dbConnections":   {}
    }
  }
}
EOF
```

**Optional API keys** — add any you have:
```json
{
  "environments": {
    "default": {
      "ollamaEndpoint":   "http://localhost:11434",
      "springgEndpoint":  "http://localhost:8181",
      "whisperCppBin":    "whisper-cli",
      "whisperCppModel":  "/Users/YOUR_NAME/whisper-models/ggml-base.en.bin",
      "piperBin":         "piper",
      "anthropicKey":     "sk-ant-...",
      "openaiKey":        "sk-...",
      "elevenlabsKey":    "...",
      "deepgramKey":      "...",
      "braveKey":         "...",
      "dbConnections":    {}
    }
  }
}
```

### lcdata.json

The `lcdata.json` in the repo root controls the server. For local use, disable JWT:

```bash
cat > lcdata.json << 'EOF'
{
  "port": 8080,
  "jwt_secret": "change-this-in-production",
  "require_jwt": false,
  "nodes_path": "./nodes",
  "store_path": "./lcdata.db",
  "env": "default",
  "log_level": "info",
  "max_concurrent_runs": 10,
  "run_timeout": "5m",
  "run_history": 100,
  "rate_limit_rps": 0,
  "rate_limit_burst": 0
}
EOF
```

> **Note:** If you expose lcdata on a network (not just localhost), set `require_jwt: true` and use a strong `jwt_secret`. Generate a token with any JWT tool or ask Claude to help.

---

## 7. Validate everything

```bash
cd lcdata
./lcdata validate
```

Expected: `All 23 nodes valid`

```bash
./lcdata list
```

You should see `voice_assistant`, `voice_stt`, `voice_llm`, `voice_tts` in the list.

---

## 8. Build the d1 voice client

```bash
cd d1
go mod tidy
go build -o d1 .
./d1 --help
```

---

## 9. Start everything

Open **four terminals** (or use tmux/screen):

```bash
# Terminal 1 — Ollama
ollama serve

# Terminal 2 — springg (from wherever you cloned it)
cd ~/path/to/springg
./springg serve

# Terminal 3 — lcdata
cd ~/path/to/lcdata
./lcdata serve

# Terminal 4 — d1 voice client
cd ~/path/to/lcdata
./d1/d1 --list-devices    # find your mic and speaker indices
./d1/d1 --mic 0 --speaker 1
```

---

## 10. First voice test

When d1 starts, you'll see:

```
[HH:MM:SS.000] d1 — lcdata voice client
[HH:MM:SS.001] Microphone  : Your Mic Name
[HH:MM:SS.001] Speaker     : Your Speaker Name
[HH:MM:SS.001] Backend     : http://localhost:8080
[HH:MM:SS.001] Node        : voice_assistant
[HH:MM:SS.050] ✓  Backend reachable

Press Enter to start recording.
Press Enter again to stop and send.

▶
```

Press **Enter**, say something, press **Enter** again. The terminal will show the transcript, the AI's text response, and play audio back through your speaker.

---

## Quick provider swap reference

All provider changes are made by editing the three voice sub-node JSON files:

| What to change | File | Field |
|---|---|---|
| LLM model | `nodes/voice_llm/voice_llm.json` | `"model"` — any model from `ollama list` |
| LLM provider → Claude | same file | `"provider": "anthropic"`, `"model": "claude-haiku-4-5"` |
| STT → whisper.cpp | `nodes/voice_stt/voice_stt.json` | `"provider": "whisper-cpp"` |
| STT → Deepgram | same file | `"provider": "deepgram"` |
| TTS → Piper | `nodes/voice_tts/voice_tts.json` | `"provider": "piper"`, `"voice_id": "/path/to/voice.onnx"` |
| TTS → ElevenLabs | same file | `"provider": "elevenlabs"`, `"voice_id": "voice-id-from-elevenlabs"` |

**lcdata hot-reloads node changes within 200ms — no restart needed.**

---

## Troubleshooting

### `./lcdata validate` fails

- Read the error — it names the node and field that's wrong.
- Most common: `voice_tts` has a Piper voice_id path that doesn't exist yet.

### whisper-cli not found

```bash
which whisper-cli
# If blank, either it's not installed or not in PATH.
# For Homebrew: brew reinstall whisper-cpp
# For source build: add the build/bin dir to PATH in your ~/.zshrc
```

### Ollama connection refused

```bash
ollama serve   # must be running
curl http://localhost:11434/api/tags   # should return JSON
```

### springg connection refused

```bash
# Make sure springg is running and listening on 8181
curl http://localhost:8181/health
```

### d1 builds but CGO errors during build

```bash
# macOS — install Xcode tools
xcode-select --install

# Linux — install build tools
sudo apt install -y build-essential
```

### JWT 401 errors from the API

Set `"require_jwt": false` in `lcdata.json` for local use, or generate a valid token:
```bash
# Generate a token (replace YOUR_SECRET with the value in lcdata.json)
node -e "
const jwt = require('jsonwebtoken');
console.log(jwt.sign({sub:'local'}, 'YOUR_SECRET', {expiresIn:'30d'}));
"
# Then pass it: ./d1/d1 --token eyJ...
```

### Voice response sounds wrong / cut off

whisper.cpp expects 16kHz mono WAV. d1 records at 16kHz by default. If you change `--sample-rate`, whisper quality may degrade. Keep it at 16000.

### Piper model file not found

```bash
ls ~/piper-voices/    # verify the .onnx and .onnx.json are both there
# The voice_id must point to the .onnx file (not the .json)
```

---

## Stack architecture

```
d1 (voice client)
  │  multipart WAV upload
  ▼
lcdata :8080
  └── voice_assistant (pipeline)
        ├── voice_stt   → whisper-cli subprocess  (16kHz WAV → transcript)
        ├── voice_llm   → ollama :11434            (transcript → response text)
        └── voice_tts   → piper subprocess         (response text → WAV audio)
  └── vector nodes     → springg :8181            (for memory/RAG pipelines)
  └── search nodes     → Brave / SearXNG          (for research pipelines)
```

All components run locally. No data leaves the machine unless you configure a cloud provider.
