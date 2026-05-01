import os, sys, json, difflib
from urllib.parse import unquote

target    = os.environ.get('TARGET', '').strip().lower()
directory = os.path.expanduser(os.environ.get('DIRECTORY', '~/Desktop'))
raw_files = os.environ.get('FILES', '[]')

try:
    files = json.loads(raw_files)
except Exception:
    sys.exit(1)

if not isinstance(files, list):
    sys.exit(1)

names = [f['name'] for f in files if isinstance(f, dict) and not f.get('is_dir', False)]

if not names or not target:
    sys.exit(1)

# 1. Exact case-insensitive match
for n in names:
    if n.lower() == target:
        print(os.path.join(directory, n)); sys.exit(0)

# 2. Stem match — ignore extension ("Hello World" matches "Hello World.txt")
for n in names:
    if os.path.splitext(n)[0].lower() == target:
        print(os.path.join(directory, n)); sys.exit(0)

# 3. URL-decoded match — "Hello World" matches "Hello%20World.txt"
for n in names:
    decoded = unquote(n)
    if decoded.lower() == target or os.path.splitext(decoded)[0].lower() == target:
        print(os.path.join(directory, n)); sys.exit(0)

# 4. Normalised match — strip spaces ("helloworld" matches "Hello World.txt")
tnorm = target.replace(' ', '')
for n in names:
    stem = os.path.splitext(n)[0].lower().replace(' ', '')
    decoded_stem = os.path.splitext(unquote(n))[0].lower().replace(' ', '')
    if tnorm == stem or tnorm in stem or target in n.lower() or tnorm == decoded_stem:
        print(os.path.join(directory, n)); sys.exit(0)

# 5. Fuzzy fallback (also tries URL-decoded names)
decoded_names = [unquote(n) for n in names]
low = [d.lower() for d in decoded_names]
m = difflib.get_close_matches(target, low, n=1, cutoff=0.4)
if m:
    for n, d in zip(names, decoded_names):
        if d.lower() == m[0]:
            print(os.path.join(directory, n)); sys.exit(0)

sys.exit(1)
