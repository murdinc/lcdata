import os, sys, json, re

raw = os.environ.get('INTENT_JSON', '').strip()

# Strip markdown code fences if present (```json ... ``` or ``` ... ```)
raw = re.sub(r'^```[a-zA-Z]*\n?', '', raw)
raw = re.sub(r'\n?```$', '', raw)
raw = raw.strip()

# If the model wrapped it in <think>...</think>, strip that too
raw = re.sub(r'<think>.*?</think>', '', raw, flags=re.DOTALL).strip()

# Try to find the first {...} JSON object in the output
m = re.search(r'\{.*\}', raw, re.DOTALL)
if m:
    raw = m.group(0)

try:
    data = json.loads(raw)
except Exception as e:
    # Fallback: return a question intent with the original message
    print(json.dumps({"intent": "question", "message": raw, "parse_error": str(e)}))
    sys.exit(0)

if not isinstance(data, dict):
    print(json.dumps({"intent": "question", "message": str(data)}))
    sys.exit(0)

# Ensure intent field exists
if 'intent' not in data:
    data['intent'] = 'question'

# If the model invented an intent name that isn't in our known set,
# it means the request needs a new capability — promote to build_capability.
KNOWN_INTENTS = {
    'delete_file', 'create_file', 'list_files', 'web_search',
    'fetch_and_save', 'question', 'build_capability'
}
if data['intent'] not in KNOWN_INTENTS:
    invented = data['intent']
    desc = f"User wants to: {invented}"
    # Carry over any useful fields the model extracted as extra context
    extras = {k: v for k, v in data.items() if k != 'intent'}
    if extras:
        desc += f". Extracted params: {json.dumps(extras)}"
    data = {'intent': 'build_capability', 'description': desc}

print(json.dumps(data))
sys.exit(0)
