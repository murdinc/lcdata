import os, sys, json, re

raw   = os.environ.get('RESULTS', '[]')
query = os.environ.get('QUERY', '').strip()

try:
    results = json.loads(raw)
except Exception:
    results = []

if not isinstance(results, list) or len(results) == 0:
    suffix = f" for \"{query}\"" if query else ""
    print(f"I wasn't able to find any results{suffix}.")
    sys.exit(0)

top  = results[0]
desc = (top.get('description') or '').strip()

# Strip "Page Title | site.com — " prefix if description starts with the page title
desc = re.sub(r'^.{10,120}[—–]\s*', '', desc).strip()

# Trim at sentence boundary around 400 chars
if len(desc) > 400:
    cut = desc[:400].rfind('.')
    desc = desc[:cut + 1] if cut > 100 else desc[:400] + '...'

if desc:
    print(desc)
else:
    title = (top.get('title') or '').strip()
    # Strip site name from title (everything after | or —)
    title = re.split(r'[|—–]', title)[0].strip()
    print(title if title else "I found a result but it had no description.")

sys.exit(0)
