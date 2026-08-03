# symbrowse {{.Version}}

{{.Summary}}

## Highlights

- _— filled by the release process —_

## Installation

```bash
brew install danieljustus/tap/symbrowse
```

Or download a signed archive (`.tar.gz` / `.zip`) from the assets below.
Every archive ships with a Syft-generated SBOM (`*.sbom`), a keyless Cosign
signature (`*.sig`) and its certificate (`*.pem`); `checksums.txt` verifies
integrity:

```bash
shasum -a 256 -c checksums.txt
cosign verify-blob --cert <archive>.pem --signature <archive>.sig <archive>
```

## Changes

{{.Changes}}
