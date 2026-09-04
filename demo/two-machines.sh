#!/usr/bin/env bash
#
# The two-machine demo: save on A, resume on B.
#
# Machine A writes a memory and a conversation through an MCP server and puts
# them on Sia. Machine B is a directory that has never existed before. It is
# given the recovery phrase and nothing else, rebuilds the vault from the
# network, and reads back the same memory and the same conversation, with a
# different MCP client, in a different language, to show the memory is not bound
# to the tool that wrote it.
#
# Run:
#   source your credentials, then  ./demo/two-machines.sh
#
# It needs SENNIT_PHRASE and SENNIT_APP_KEY in the environment. Neither is
# ever printed, passed as an argument, or written to a file by this script.
#
# ⚠ Nothing here prints the recovery phrase. `set -x` would, which is why this
# script does not use it and why every command that carries a secret gets it
# from the environment rather than from a flag.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="${SENNIT_DEMO_DIR:-$(mktemp -d -t sennit-demo-XXXXXX)}"
bin="$work/bin"
keep="${SENNIT_DEMO_KEEP:-}"

: "${SENNIT_PHRASE:?set SENNIT_PHRASE in the environment (never as an argument)}"
: "${SENNIT_APP_KEY:?set SENNIT_APP_KEY, it is issued by \`sennit connect\`}"

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
note() { printf '  \033[2m%s\033[0m\n' "$*"; }

cleanup() {
  if [ -n "$keep" ]; then
    note "left $work in place"
    return
  fi
  bold "Releasing the storage this demo wrote"
  # Forgetting comes first and reclaiming second, and the order is not a
  # preference: a slab is billed whole and comes back only once nothing live is
  # left in it, so reclaiming without forgetting frees exactly nothing.
  SENNIT_HOME="$work/a" "$bin/agent" -server "$bin/sennit-mcp" -forget-all 2>&1 | sed 's/^/  /' || true
  SENNIT_HOME="$work/a" "$bin/sennit" reclaim 2>&1 | sed 's/^/  /' || true
  rm -rf "$work"
}
trap cleanup EXIT

bold "Building"
mkdir -p "$bin"
( cd "$root" && CGO_ENABLED=0 go build -o "$bin/sennit" ./cmd/sennit )
( cd "$root" && CGO_ENABLED=0 go build -o "$bin/sennit-mcp" ./cmd/sennit-mcp )
( cd "$root" && CGO_ENABLED=0 go build -o "$bin/agent" ./demo/agent )
if [ ! -d "$root/demo/cross-agent/node_modules" ]; then
  ( cd "$root/demo/cross-agent" && npm install --silent )
fi
note "sennit, sennit-mcp and two MCP clients"

# The clock starts here. Building is not the demo, and neither is installing a
# client's dependencies; both are one-time and neither says anything about
# whether a memory follows its owner.
# ── Machine A ────────────────────────────────────────────────────────────────
bold "Machine A, an agent stores a memory and a conversation"
start=$SECONDS
SENNIT_HOME="$work/a" "$bin/agent" \
  -server "$bin/sennit-mcp" \
  -remember "The one-hour flush cap bounds the durability window, not cost; repack is what controls cost." \
  -context "Settled while working out the packer's flush policy, after measuring that a partially filled slab can never be extended." \
  -tags "storage,flush,decisions" \
  -session "Deciding the flush cadence" \
  -summary "Worked out why the one-hour cap exists: it bounds how long a turn lives only on the device. Cost is controlled by repack instead."

SENNIT_HOME="$work/a" "$bin/sennit" flush 2>&1 | sed 's/^/  /'
wrote=$(( SECONDS - start ))
note "on Sia after ${wrote}s"

# ── Machine B ────────────────────────────────────────────────────────────────
bold "Machine B, a directory that has never held this vault"
note "$work/b, no catalog, no index, no bodies, no session heads"
SENNIT_HOME="$work/b" "$bin/sennit" hydrate -depth index 2>&1 | sed 's/^/  /'
hydrated=$(( SECONDS - start ))

bold "Machine B, read back, with a different MCP client"
note "TypeScript MCP SDK, talking to the same stdio server the Go client used"
SENNIT_HOME="$work/b" \
CROSS_AGENT_QUERY="why is there a one hour cap on flushing" \
SENNIT_MCP_BIN="$bin/sennit-mcp" \
  node "$root/demo/cross-agent/read-vault.mjs"

total=$(( SECONDS - start ))
bold "Done in ${total}s, write ${wrote}s · hydrate $(( hydrated - wrote ))s · read $(( total - hydrated ))s"
cat <<'TEXT'
  The memory and the transcript came back byte for byte from the network.
  What did NOT come back is the conversation's own description, its summary,
  tags, project and lineage live only on the device that wrote them, so machine
  B rebuilt the head from the transcript and its title is reconstructed rather
  than restored. The field-by-field breakdown is in demo/README.md.
TEXT
