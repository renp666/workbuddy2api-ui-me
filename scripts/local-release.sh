#!/usr/bin/env bash
# 本机发版：构建 -> 滚动钉版 -> 一条命令重启 -> 清理。生产入口不受影响（docker-compose.yml 不动）。
#
#   bash scripts/local-release.sh [--allow-dirty]
#
# 构建上下文是 `git archive HEAD`，不是工作区，所以未提交的改动不会进镜像：脚本默认在工作区
# 脏时拒绝执行，确认要带上未提交内容用 --allow-dirty（自己知道在发什么）。
# 镜像标签滚动规则：:local = 当前在用，:local-prev = 上一个可回退版本，只留这两个。
set -euo pipefail

cd "$(dirname "$0")/.."

if git status --porcelain | grep -q . && [[ "${1:-}" != "--allow-dirty" ]]; then
  echo "工作区有未提交改动，而构建上下文是 git archive HEAD（这些改动不会进镜像）。" >&2
  git status --short >&2
  echo "先提交，或确认无误后加 --allow-dirty。" >&2
  exit 1
fi

docker info >/dev/null 2>&1 || { echo "Docker 没在运行。" >&2; exit 1; }
mkdir -p .build
test -f docker-compose.override.yml || echo "注意：缺 docker-compose.override.yml，up -d 会退回 docker-compose.yml 里钉的已发布镜像对。" >&2

# 上下文落在仓库内而不是 /tmp：git.exe 看不见 MSYS 的 /tmp，写成相对路径才稳。
CTX=".build/release-ctx.tar"
trap 'rm -f "$CTX"' EXIT
git archive --format=tar -o "$CTX" HEAD
echo "CTX $(wc -c < "$CTX") bytes from $(git log -1 --format='%h %s')"

# 旧 :local 降级为 :local-prev（覆盖掉更早的那一个，被覆盖的镜像随后由 prune 清掉）。
for pair in "core:deploy/core.Dockerfile" "console:deploy/console.Dockerfile"; do
  svc="${pair%%:*}"
  docker image inspect "workbuddy2api-${svc}:local" >/dev/null 2>&1 \
    && docker tag "workbuddy2api-${svc}:local" "workbuddy2api-${svc}:local-prev" \
    && echo "DEMOTED workbuddy2api-${svc}:local -> :local-prev"
done

for pair in "core:deploy/core.Dockerfile" "console:deploy/console.Dockerfile"; do
  svc="${pair%%:*}"
  image="workbuddy2api-${svc}:local"
  echo "BUILD ${image}"
  docker build -f "${pair#*:}" -t "${image}" --build-arg GOPROXY=https://goproxy.cn,direct \
    --pull=false - < "$CTX" >".build/${svc}-release.log" 2>&1 \
    || { echo "构建失败，见 .build/${svc}-release.log" >&2; tail -20 ".build/${svc}-release.log" >&2; exit 1; }
  echo "OK ${image} $(docker image inspect --format '{{.Id}}' "${image}" | cut -c1-19)"
done

# 一条命令把五只服务（core/console + 三只旁路）一起对齐到 :local。
docker compose up -d

docker image prune -f >/dev/null
echo
docker ps --filter 'name=workbuddy2api-ui-me' --format '{{.Names}}\t{{.Image}}\t{{.Status}}'
curl -fsS -o /dev/null -w 'healthz=%{http_code}\n' --max-time 10 http://127.0.0.1:7863/healthz
echo "回退：docker tag workbuddy2api-console:local-prev workbuddy2api-console:local && docker tag workbuddy2api-core:local-prev workbuddy2api-core:local && docker compose up -d"
