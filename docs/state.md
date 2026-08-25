# State Encryption

Symaira Browse stores browser session state (cookies, local storage, session
storage) under `<state-dir>/states/`. By default, files are written as
**plaintext JSON** so the tool works out of the box without any key setup.

When an encryption key is available, state files are transparently encrypted
with AES-256-GCM. Version 3 stores a small plaintext metadata header followed
by the encrypted body, and authenticates that header as additional authenticated
data. Existing version 1 and 2 files remain readable; saving them upgrades the
file to version 3.

## Key resolution order

At daemon startup, `NewKeyResolver` probes the following sources in order and
uses the first one that yields a valid 32-byte key:

| Priority | Source | How it works |
|----------|--------|--------------|
| 1 | **symvault** | If the `symvault` binary is on `$PATH` and the entry `symbrowse/encryption-key` exists, its value is used. |
| 2 | **OS keychain** | On macOS, `security find-generic-password -s symbrowse -a encryption-key` is called. The value must be a 64-character hex string (or a raw 32-byte key). Other platforms skip this source. |
| 3 | **Environment variable** | `SYMBROWSE_ENCRYPTION_KEY` must hold a 64-character hex string (32 bytes). This is the documented fallback. |

If none of the sources provides a key, state files are stored as plaintext.
`state show` and `state list` report the active `key_source` field so you can
verify whether encryption is active.

## Enabling encryption

The simplest path is the OS keychain, which requires no environment variable:

```bash
# Store a 32-byte random key as 64-character hex in the macOS keychain
KEY=$(openssl rand -hex 32)
security add-generic-password -s symbrowse -a encryption-key -w "$KEY"
```

After that, restart the daemon — `state show` will report
`key_source: "keychain"` and all subsequent saves will be encrypted.

Alternatively, set the environment variable (e.g. in your shell profile):

```bash
export SYMBROWSE_ENCRYPTION_KEY=$(openssl rand -hex 32)
```

Or store the key in [symvault](https://github.com/danieljustus/symaira-vault)
under the entry name `symbrowse/encryption-key`.

## Plaintext fallback

When no key source is available, saving still succeeds — the file is written
as plain JSON. `state show` reports `key_source: "none"` so the absence of
encryption is visible, not silent.

## Migration

There is no background migration. Existing version 1 and 2 files are readable
and are upgraded to version 3 when saved. Existing plaintext files remain
plaintext until they are re-saved with an active key. To re-save all states:

```bash
for name in $(symbrowse state list --json | jq -r '.data.states[].name'); do
  symbrowse state save "$name" --session <session>
done
```

## On-disk format

```
SYMBROWSE-STATE\0   (16-byte magic prefix)
<JSON metadata header>\n
<encrypted or plaintext body>
```

The header contains only `schema_version`, `saved_at`, `expires_at` and
`key_source`. For encrypted version 3 files, the exact header bytes are bound
to the ciphertext with AES-256-GCM authentication. Editing retention metadata
or changing `key_source` therefore makes loading, expiry checks and cleanup
fail instead of silently accepting forged state. Plaintext files remain
available as the no-key fallback, but do not provide cryptographic integrity.
