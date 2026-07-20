# Chronicler manuscript archives (`.chron`)

The Global Config **Manuscript Archive** controls export and import a portable
copy of the authenticated user's manuscript library. A `.chron` archive is not
a SQLite backup and importing one never replaces the server database.

## Version 1 format

Version 1 is a standard ZIP container:

```text
manifest.json
manuscripts/000001.json
manuscripts/000002.json
covers/000001.webp
```

`manifest.json` identifies `ink.chronicler.manuscripts`, format version `1`,
the `zip-deflate` codec, and every payload path. Manuscript JSON retains chapter
HTML—including legacy comments and audio annotations—and all metadata understood
by the server. Cover images are included but stored without another compression
pass because PNG, JPEG, and WebP are already compressed.

The server uses balanced Deflate (level 6). It gives prose gzip-class compression
without XZ's potentially long CPU-heavy export. A representative 360,000-word
test library must produce a `.chron` smaller than 5 MiB. Real size depends on the
text and especially on cover art; multi-million-word archives are supported up to
the documented import safety limits.

Imports are additive and account-scoped. IDs are preserved when available. If an
ID already exists, Chronicler generates a new ID and labels the title as an
imported copy. It never silently overwrites the existing manuscript. Compressed
input is limited to 256 MiB, expanded input to 2 GiB, and each manuscript payload
to 512 MiB to reject ZIP bombs while leaving ample room for very large libraries.

## Future compression-codec plugin

TODO: add an `archiveCodec` contribution to a future plugin API so an installed
plugin can offer additional export/import codecs while the built-in
`zip-deflate` path remains the dependable default. Candidate choices:

- Zstandard for faster large-library exports and decompression with a strong
  compression ratio.
- XZ/LZMA for an explicit “smallest archive” mode where the writer accepts
  substantially more CPU time and memory.
- Gzip for simple streaming interoperability.

The plugin must declare its codec in the manifest, use a distinct versioned
container contract, enforce the same expanded-size/path validation, and remain
optional. Core must never require an external codec executable merely to read its
default `.chron` files.
