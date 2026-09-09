# Bundled zlib archives

The checked-in headers identify zlib **1.3.1**. `artifacts.json` records SHA-256
hashes of both headers and every existing target archive. Historical source
revisions, compiler versions and build flags were not recorded; their entries
are deliberately null. Do not treat the header version or this inventory as a
verified build attestation for an archive.

The security/streaming fixes in this revision do not replace these binaries.

## Verify the inventory

Run from the repository root:

```sh
python3 - <<'PY'
import hashlib, json
from pathlib import Path
root = Path('zlib')
m = json.loads((root / 'artifacts.json').read_text())
for name, expected in m['headers_sha256'].items():
    assert hashlib.sha256((root / name).read_bytes()).hexdigest() == expected, name
for a in m['archives']:
    assert hashlib.sha256((root / a['file']).read_bytes()).hexdigest() == a['sha256'], a['file']
print('All bundled artifacts match the inventory.')
PY
```

## Rebuild and update

Use an authenticated upstream zlib source release from <https://zlib.net/> or
an explicitly pinned commit from <https://github.com/madler/zlib>. Record the
source archive SHA-256 or commit before building. Do not silently substitute a
system zlib library or mix new headers with unverified old archives.

For each target, use a clean source directory and the appropriate toolchain:

```sh
# Set CC, AR and RANLIB to the target tools, as in the main README.
# ZLIB_INSTALL must be an absolute temporary output directory.
CFLAGS='-O2 -fPIC' ./configure --static --prefix="$ZLIB_INSTALL"
make
make check  # Run on a compatible target host, not the cross-compilation host.
make install
```

On Windows, use flags supported by the selected MinGW toolchain. A successful
cross-compilation alone does not verify that an archive runs on the target.

Before replacing an archive:

1. Record source version and hash/commit, compiler version, exact configure and
   build commands, flags, target OS/architecture and resulting archive SHA-256.
2. Verify the linked `zlibVersion()` on the target and run this project's full
   tests and regression suite there. Run race tests where supported, and fuzz
   the raw decoder and concatenation paths on supported development hosts.
3. Replace the target archive and update its manifest entry. Update shared
   headers consistently when upgrading the source release. Retain the exact
   build records in the manifest instead of overwriting unknown fields with
   guesses. Run the inventory check and review all binary changes.
4. Check upstream security advisories and the relevant release notes before
   shipping an update; a version inventory alone is not a vulnerability audit.
