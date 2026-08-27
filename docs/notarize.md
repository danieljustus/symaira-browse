# macOS notarization

Release notarization uses the App Store Connect API-key flow. It does **not**
use the Apple ID flow: `NOTARY_API_KEY_ID` is a key ID, and
`NOTARY_API_ISSUER` is an issuer UUID, not an Apple ID email or a Team ID.

The release workflow decodes the base64-encoded `.p8` key into a temporary file
and invokes:

```sh
xcrun notarytool submit <zip> \
  --key <temporary .p8 path> \
  --key-id "$NOTARY_API_KEY_ID" \
  --issuer "$NOTARY_API_ISSUER" \
  --wait
```

## Secret mapping

| GitHub Actions secret | SymVault entry | Stored value | `notarytool` use |
|---|---|---|---|
| `NOTARY_API_KEY` | `notary-api-key` | Base64-encoded App Store Connect `.p8` key; do not re-encode it | Decode to a temporary file passed to `--key` |
| `NOTARY_API_KEY_ID` | `key-id` | App Store Connect key ID | `--key-id` |
| `NOTARY_API_ISSUER` | `issuer-id` | App Store Connect issuer UUID | `--issuer` |

The temporary key file is mode `0600` and is removed when the notarization step
finishes. Missing secrets skip notarization with a warning; invalid base64 key
material fails the release rather than submitting an ambiguous command.

The `--apple-id` flow is a different setup. It requires an Apple ID email, a
10-character Team ID, and an app-specific password; those values must never be
mapped from the App Store Connect API-key entries above.
