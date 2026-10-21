# Operator Runbook

Operational notes for running Tallybook's services and for deploying and
managing the on-chain state they depend on. This starts with the first
hazard found in practice, deploying a `one-way-channel` instance; more
sections will accumulate here as the rest of the system (collector,
settler, indexer) is built and operated.

## ⚠️ `one-way-channel`: `BytesN<32>` arguments must be hex, never a strkey address

**Symptom:** `stellar contract deploy` (or `stellar contract invoke`)
fails with a trap on `__constructor` (or whichever function you called),
even against an unmodified, correctly-built contract:

```
❌ error: transaction simulation failed: HostError: Error(Context, InvalidAction)
...
["VM call trapped: UnreachableCodeReached", "__constructor"]
```

**Cause:** a `stellar-cli 28.0.0` argument-encoding bug, not a bug in
`one-way-channel` or in Tallybook. Any `BytesN<32>` CLI argument —
`one-way-channel`'s `commitment_key` in particular — traps the WASM call
the instant the contract touches it if supplied as a **G-address strkey**
(e.g. the output of `stellar keys generate` / `stellar keys
public-key`). The identical 32 bytes supplied as a **hex string** work
correctly on the byte-identical wasm. This was verified against a
from-scratch control contract (isolating the bug down to a single
`BytesN<32>` parameter, independent of `one-way-channel`'s own code) and
confirmed by deploying the real, unmodified `one-way-channel` contract
both ways. Full reproduction, every command and diagnostic included, is
the session artifact `/tmp/one-way-channel-trap.md` (not committed to
this repository — regenerate it if you need the full trail again).

`one-way-channel`'s own `examples/demo.sh` never hits this, because its
bundled `ed25519` tool always emits the commitment key as hex.

**Fix:** generate `commitment_key` (and any other `BytesN<32>` argument)
as hex — e.g. via `openssl rand -hex 32`, or the `ed25519` tool bundled
with `one-way-channel` — never via `stellar keys generate` or anything
else that emits a strkey address.

**Verified working deploy** (testnet, `stellar-cli 28.0.0`, real
`one-way-channel` at commit `25dea1b`, wasm hash
`ee5dca12e4e9746e7e241b8bfbf507444a1dbbca21ee2d6fca8005d9654513bd`):

```sh
stellar contract deploy \
    --alias owc-repro-hexfix \
    --wasm-hash ee5dca12e4e9746e7e241b8bfbf507444a1dbbca21ee2d6fca8005d9654513bd \
    --source tb-operator \
    --network testnet \
    -- \
    --token CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC \
    --from tb-operator \
    --commitment_key 9a3cfafc6a218a5459c1978146d6c41638a60f34a81314d02f52ec49d080ec7e \
    --to tb-consumer \
    --amount 10000000 \
    --refund_waiting_period 5
```

`--commitment_key` above is hex, not a `G...` address — that's the entire
fix. This deployed successfully and was confirmed live afterward with
correct `token` / `from` / `deposited` / `balance` / `refund_waiting_period`
reads against the new instance.

## Fix status (upstream stellar-cli)

I checked every stellar-cli release newer than `28.0.0` — the latest is
`v28.1.0` (released 2026-09-26) — and none of their release notes or commits
mention a fix for the `BytesN<32>`-as-strkey encoding trap. The bug is not
confirmed fixed in any stellar-cli version newer than `28.0.0`, so the
hex-only workaround above remains required: supply `commitment_key` (and any
other `BytesN<32>` argument) as hex, never as a `G...` strkey address.

If you confirm the bug is fixed in a newer stellar-cli, update this section
to note the fixed version and that the hex workaround is no longer required,
and verify with the deploy command above (commitment_key as hex, then as a
strkey — the strkey form should trap until the fix ships).
