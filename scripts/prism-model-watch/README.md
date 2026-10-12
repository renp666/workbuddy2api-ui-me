# prism-model-watch

Watchdog for the Prism gateway (workbuddy2api) **model alias rotation**.

The dispatch chain pins `WF_TRAE_MODEL` to a gateway alias (currently
`1-daily-v1`). Upstream silently re-points or drops aliases, which has twice
caused the dispatch chain to fail 3 cards in a row. This watchdog catches the
change first and, optionally behind a gate, re-pins the model.

## What it checks

1. **Membership** - an alias present in the baseline disappeared, or a new alias appeared.
2. **Routing** - an alias resolves to a different `routed_model` than the baseline.
3. **Liveness** - a minimal real `POST /v1/chat/completions` (`max_tokens=1`) succeeds.

## Exit codes

| code | meaning |
|------|---------|
| 0 | all green (silent unless `--verbose`) |
| 1 | alias set / routing baseline changed |
| 2 | one or more alias probes failed |
| 3 | gateway unreachable or config error |

## Usage

```sh
# one-time: record the current alias routing as the baseline
python watch.py --update-baseline

# routine check (this is what cron runs)
python watch.py

# gated auto-repin: if WF_TRAE_MODEL's alias fails, dry-run a re-pin
python watch.py --auto-repin
# ...and actually write it
python watch.py --auto-repin --apply
```

The API key is read from `WB2A_API_KEY` (or `--key-file`). Never hardcode it.

## Files

- `watch.py` - the watchdog.
- `baseline.json` - alias -> {realm, routed_model} snapshot (created by `--update-baseline`).

## Deploy / cron

Source of truth is this repo. Deploy a copy to the Hermes scripts directory:

```sh
cp -r scripts/prism-model-watch "$HERMES_HOME/scripts/"
```

Register as a resident no-agent cron job (stdout is delivered verbatim; empty = silent):

```sh
"$HERMES_HOME/hermes-agent/venv/Scripts/hermes.exe" cron create "*/15 * * * *" \
  --name prism-model-watch --script prism-model-watch/watch.py --no-agent \
  --workdir "$HERMES_HOME/scripts"
```

## Gated auto-repin

Auto-repin only triggers when `--auto-repin` is passed **and** the currently
pinned `WF_TRAE_MODEL` alias is exactly one of the failing aliases. It picks the
first healthy alias as the fallback and rewrites `config.env`. Default is
dry-run; `--apply` performs the write. The gate prevents thrashing.

## Known limitations

- Billed aliases (`3-strong-v1`, `3-strong-v2`) are never liveness-probed (the user's standing rule forbids using that group without consent); their membership/routing changes are still detected from `/v1/models`.
- Aliases switched off in the console answer `404 model_disabled`; these are reported as `disabled` (an admin choice, not a rotation failure) and do not raise the exit code -- unless the pinned `WF_TRAE_MODEL` alias is one of them, which is a real dispatch breakage.
- `baseline.json` is generated state: auto-created on first run when absent, and not meant to be hand-edited. Keep the repo copy only for reference; each deployed copy keeps its own.
- The fallback choice is naive (first healthy alias, not ranked by cost/realm).
- It edits `hermes-wf-gofish/config.env` only; other chains must be added explicitly.
