# plugins-seed

Empty by design. The official image ships no bundled plugins — they are
installed from a git URL in the UI.

This directory is a hook for building a customised or air-gapped image: drop a
plugin's source here and the server copies it into `/data/plugins` and compiles
it on first boot (see `pkg/plugins/seed.go`).
