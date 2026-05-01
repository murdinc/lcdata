import os, sys, json, re

raw = os.environ.get('DESIGN_JSON', '').strip()

# Strip markdown code fences
raw = re.sub(r'^```[a-zA-Z]*\n?', '', raw)
raw = re.sub(r'\n?```$', '', raw)
raw = raw.strip()

# Strip <think>...</think>
raw = re.sub(r'<think>.*?</think>', '', raw, flags=re.DOTALL).strip()

# Find first { ... } block
m = re.search(r'\{.*\}', raw, re.DOTALL)
if m:
    raw = m.group(0)

try:
    data = json.loads(raw)
except Exception as e:
    print(json.dumps({"error": f"Failed to parse design JSON: {e}", "raw": raw[:500]}))
    sys.exit(0)

if not isinstance(data, dict):
    print(json.dumps({"error": "Design output is not a JSON object", "raw": raw[:200]}))
    sys.exit(0)

# Extract special fields before treating rest as node config
scripts = data.pop('_scripts', {})
test_input = data.pop('_test_input', {})

name = data.get('name', '')
if not name:
    print(json.dumps({"error": "Design missing 'name' field"}))
    sys.exit(0)

print(json.dumps({
    "name": name,
    "config": data,
    "scripts": scripts,
    "test_input": test_input
}))
sys.exit(0)
