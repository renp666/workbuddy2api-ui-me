#!/usr/bin/env bash
# Read-only channel diagnostic: are the gateway and all four channels serving right now?
# Touches no data, restarts nothing, prints no key.
#
# The sidecars share console's network namespace (network_mode: service:console), so their ports
# are invisible to the host loopback and must be probed from inside that namespace. Node comes from
# an image this project already builds; `sh` inside console has no /dev/tcp and would report every
# port CLOSED. Port sources of truth: deploy/compose.{zcode,qoder,opencode}.yml.
set -euo pipefail
cd "$(dirname "$0")/.."

console="$(docker ps --format '{{.Names}}' | grep -- '-console-1$' | head -1)"
if [[ -z "$console" ]]; then
  echo "no running console container; nothing to probe" >&2
  exit 1
fi

probe_image="${WB2A_PROBE_IMAGE:-workbuddy2api-ui-me-qoder-proxy}"
if ! docker image inspect "$probe_image" >/dev/null 2>&1; then
  echo "probe image $probe_image missing; set WB2A_PROBE_IMAGE to any node-based local image" >&2
  exit 1
fi

echo "== gateway (host loopback, published by console)"
printf '  healthz=%s livez=%s\n' \
  "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:7863/healthz" || echo 000)" \
  "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:7863/livez" || echo 000)"

echo "== sidecar ports (inside $console network namespace)"
# Waiting for glm- is the point: after a cold boot the zcode data plane takes a few seconds.
docker run --rm --network "container:${console}" --entrypoint node "$probe_image" -e '
  const net = require("net");
  const checks = [
    ["zcode  data 8080 /v1/models", 8080, "/v1/models", (b) => b.includes("glm-") ? "serves glm-" : "no glm- yet"],
    ["zcode  ctrl 9090 /control",   9090, "/control",   (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
    ["qoder  data 3000 /health",    3000, "/health",    (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
    ["opencode data 41980 /health", 41980, "/health",   (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
  ];
  const deadline = Date.now() + Number(process.env.WB2A_PROBE_WAIT || 30) * 1000;
  const once = ([label, port, path, read]) => new Promise((resolve) => {
    const s = net.connect(port, "127.0.0.1");
    s.setTimeout(3000);
    s.on("connect", () => {
      // The sidecars enforce loopback-only Host; a made-up Host gets a 403 that looks like an
      // outage. Mirror what console itself sends.
      s.write(`GET ${path} HTTP/1.1\r\nHost: 127.0.0.1:${port}\r\nConnection: close\r\n\r\n`);
      let body = "";
      s.on("data", (c) => { body += c; });
      s.on("close", () => resolve(`${label}  ${read(body)}`));
    });
    s.on("timeout", () => { s.destroy(); resolve(`${label}  TIMEOUT`); });
    s.on("error", (e) => resolve(`${label}  ${e.code}`));
  });
  (async () => {
    let pending = checks, round = 0;
    while (pending.length && Date.now() < deadline) {
      const results = await Promise.all(pending.map(once));
      if (round++ === 0) results.forEach((r) => console.log("  " + r));
      pending = checks.filter((c, i) => /CLOSED|ECONNREFUSED|TIMEOUT|no glm-/.test(results[i]));
      if (pending.length) await new Promise((r) => setTimeout(r, 2000));
    }
    for (const c of pending) console.log("  " + (await once(c)));
  })();
'

echo "== merged model list served by the gateway"
config="${WB2A_KEYS_FILE:-runtime/wb2api/keys/keys.json}"
if [[ -f "$config" ]]; then
  WB2A_KEYS_FILE="$config" python3 -c '
import json, os, urllib.request
key = json.load(open(os.environ["WB2A_KEYS_FILE"]))["api_key"]
req = urllib.request.Request("http://127.0.0.1:7863/v1/models",
                            headers={"Authorization": "Bearer " + key})
ids = [m["id"] for m in json.load(urllib.request.urlopen(req, timeout=30))["data"]]
realms = {"glm-": "zcode", "qoder-": "qoder", "opencode-": "opencode"}
seen = {v: 0 for v in realms.values()}
seen["workbuddy (core)"] = 0
for i in ids:
    for prefix, realm in realms.items():
        if i.startswith(prefix):
            seen[realm] += 1
            break
    else:
        seen["workbuddy (core)"] += 1
print("  total=%d " % len(ids) + "  ".join("%s=%d" % kv for kv in seen.items()))
'
else
  echo "  skipped: $config not present (pass WB2A_KEYS_FILE)"
fi
