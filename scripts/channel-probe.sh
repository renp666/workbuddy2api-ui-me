#!/usr/bin/env bash
# Read-only channel diagnostic: are the gateway and every registered channel serving right now?
# Touches no data, restarts nothing, prints no key.
#
# The sidecars share console's network namespace (network_mode: service:console), so their ports
# are invisible to the host loopback and must be probed from inside that namespace. Node comes from
# an image this project already builds; `sh` inside console has no /dev/tcp and would report every
# port CLOSED. Which channels exist is derived below from deploy/compose.*.yml, not from this list —
# a channel that ships an overlay but is missing here fails loudly instead of being silently skipped.
set -euo pipefail
cd "$(dirname "$0")/.."

# Drift guard: every sidecar overlay (identified by the very property that makes it probe-able this
# way — it joins console's namespace) must have at least one check below. Reads this script's own
# text as the list of covered names, so there is no second enumeration to keep in sync.
covered="$(sed -n 's/^    \["\([a-z][a-z0-9]*\)[ "].*/\1/p' "$0" | sort -u)"
missing=""
for overlay in $(grep -l 'network_mode: service:console' deploy/compose.*.yml 2>/dev/null | sort); do
  channel="$(basename "$overlay" .yml)"
  channel="${channel#compose.}"
  printf '%s\n' "$covered" | grep -qx "$channel" || missing="$missing $channel"
done
if [[ -n "$missing" ]]; then
  echo "channel-probe: overlay 已存在但探针清单没登记:$missing" >&2
  echo "  在下面的 checks 里补一条（端口见该 compose 文件的 LISTEN/PORT），否则这条通道永远不会被诊断到。" >&2
  exit 1
fi
# 同一份名单还要出现在「合并目录按前缀计数」里，否则新通道在端口段看得见、在榜单段永远隐形。
counted="$(sed -n 's/^realms = .*$/&/p' "$0" | grep -oE '": "[a-z][a-z0-9]*' | sed 's/.*: "//' | sort -u)"
unlisted=""
for channel in $covered; do
  printf '%s\n' "$counted" | grep -qx "$channel" || unlisted="$unlisted $channel"
done
if [[ -n "$unlisted" ]]; then
  echo "channel-probe: 探针覆盖了这些通道，但合并目录计数里没有它们:$unlisted" >&2
  echo "  在 realms 里补上各自的前缀映射，否则这一节会稳定少报这几条通道。" >&2
  exit 1
fi

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
    // autoclaw /healthz answers 200 with accounts or 503 without; both prove the port serves.
    ["autoclaw data 7865 /healthz", 7865, "/healthz",   (b) => { const m = b.match(/\{"healthy":(\d+),"total":(\d+)\}/); return m ? `healthy ${m[1]}/${m[2]}` : (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]; }],
    // trae /healthz answers plain-text "ok" (not JSON); any HTTP status proves the port serves.
    ["trae   data 7866 /healthz",  7866, "/healthz",   (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
    // raccoon /healthz is plain-text "ok" like trae; any HTTP status proves the port serves.
    ["raccoon data 7867 /healthz", 7867, "/healthz",   (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
    // traeai 与国内版共用同一镜像，只换 TRAE_EDITION=intl；/healthz 同样是纯文本 ok。
    ["traeai  data 7869 /healthz", 7869, "/healthz",   (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
    // freellm 没有 /healthz。/livez 答进程能否服务，/readyz 用 503 + 机器可读原因档答
    // 池里有没有此刻可服务的上游——目录面（/v1/models）进 sidecar 的分钟限流器，探针不碰。
    ["freellm live 7868 /livez",   7868, "/livez",    (b) => (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]],
    ["freellm pool 7868 /readyz",  7868, "/readyz",   (b) => { const m = b.match(/"reason":"([^"]+)"/); return m ? `not-ready ${m[1]}` : (b.match(/HTTP\/1\.[01] (\d+)/) || [,"?"])[1]; }],
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
realms = {"glm-": "zcode", "qoder-": "qoder", "opencode-": "opencode", "autoclaw-": "autoclaw", "trae-": "trae", "traeai-": "traeai", "raccoon-": "raccoon", "freellm-": "freellm"}
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
