import os, sys, json

raw   = os.environ.get('FILES', '[]')
direc = os.environ.get('DIRECTORY', '').strip() or 'the folder'

try:
    files = json.loads(raw)
except Exception:
    print("Unable to read the file list.")
    sys.exit(0)

if not isinstance(files, list):
    print("Unable to read the file list.")
    sys.exit(0)

# Separate files and directories
dirs  = [f['name'] for f in files if isinstance(f, dict) and f.get('is_dir')]
items = [f['name'] for f in files if isinstance(f, dict) and not f.get('is_dir')]
total = len(items)

# Friendly directory label
label = direc.replace('~/', '').replace('~', 'home').rstrip('/')

if total == 0:
    print(f"There are no files in {label}.")
    sys.exit(0)

# Show up to 5 names
shown  = items[:5]
listed = ', '.join(shown)
more   = total - len(shown)

if more > 0:
    msg = f"There are {total} files in {label}: {listed}, and {more} more."
else:
    msg = f"There {'is' if total == 1 else 'are'} {total} {'file' if total == 1 else 'files'} in {label}: {listed}."

print(msg)
sys.exit(0)
