#!/usr/bin/env bash
# 本机发版：构建 -> 滚动钉版 -> 一条命令重启 -> 清理。生产入口不受影响（docker-compose.yml 不动）。
#
#   bash scripts/local-release.sh [--allow-dirty] [--tag SUFFIX]
#
# 构建上下文是 `git archive HEAD`，不是工作区，所以未提交的改动不会进镜像：脚本默认在工作区
# 脏时拒绝执行，确认要带上未提交内容用 --allow-dirty（自己知道在发什么）。
# 镜像标签滚动规则：:local = 当前在用，:local-prev = 上一个可回退版本，只留这两个。
#
# --tag SUFFIX 是演练环境（prism-lab）用的分叉口：只构建 :SUFFIX，不滚动 :local/:local-prev、
# 不重启生产、不碰生产数据目录：
#   bash scripts/local-release.sh --tag lab
#   docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml up -d
set -euo pipefail

cd "$(dirname "$0")/.."

TAG=local
ALLOW_DIRTY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --allow-dirty) ALLOW_DIRTY=1 ;;
    --tag) TAG="${2:?--tag 需要标签后缀，例如 lab}"; shift ;;
    --tag=*) TAG="${1#--tag=}" ;;
    *) echo "未知参数：$1（可用：--allow-dirty --tag SUFFIX）" >&2; exit 2 ;;
  esac
  shift
done
# Docker 标签必须小写，所以这里只放行小写字母数字与 . _ -。
[[ "$TAG" =~ ^[a-z0-9][a-z0-9._-]*$ ]] \
  || { echo "非法标签后缀：$TAG（只允许小写字母数字与 . _ -，且不能以符号开头）" >&2; exit 2; }

if git status --porcelain | grep -q . && [[ "$ALLOW_DIRTY" -ne 1 ]]; then
  echo "工作区有未提交改动，而构建上下文是 git archive HEAD（这些改动不会进镜像）。" >&2
  git status --short >&2
  echo "先提交，或确认无误后加 --allow-dirty。" >&2
  exit 1
fi

docker info >/dev/null 2>&1 || { echo "Docker 没在运行。" >&2; exit 1; }
mkdir -p .build
if [[ "$TAG" == local ]]; then
  test -f docker-compose.override.yml || echo "注意：缺 docker-compose.override.yml，up -d 会退回 docker-compose.yml 里钉的已发布镜像对。" >&2
fi

# 上下文落在仓库内而不是 /tmp：git.exe 看不见 MSYS 的 /tmp，写成相对路径才稳。
CTX=".build/release-ctx.tar"
trap 'rm -f "$CTX"' EXIT
git archive --format=tar -o "$CTX" HEAD
echo "CTX $(wc -c < "$CTX") bytes from $(git log -1 --format='%h %s')"

# 旧 :local 降级为 :local-prev（覆盖掉更早的那一个，被覆盖的镜像随后由 prune 清掉）。
# 演练标签不做滚动：它没有「上一个可回退版本」的语义，也不该动生产的 :local。
if [[ "$TAG" == local ]]; then
  for pair in "core:deploy/core.Dockerfile" "console:deploy/console.Dockerfile"; do
    svc="${pair%%:*}"
    docker image inspect "workbuddy2api-${svc}:local" >/dev/null 2>&1 \
      && docker tag "workbuddy2api-${svc}:local" "workbuddy2api-${svc}:local-prev" \
      && echo "DEMOTED workbuddy2api-${svc}:local -> :local-prev"
  done
fi

for pair in "core:deploy/core.Dockerfile" "console:deploy/console.Dockerfile"; do
  svc="${pair%%:*}"
  image="workbuddy2api-${svc}:${TAG}"
  echo "BUILD ${image}"
  docker build -f "${pair#*:}" -t "${image}" --build-arg GOPROXY=https://goproxy.cn,direct \
    --pull=false - < "$CTX" >".build/${svc}-release.log" 2>&1 \
    || { echo "构建失败，见 .build/${svc}-release.log" >&2; tail -20 ".build/${svc}-release.log" >&2; exit 1; }
  echo "OK ${image} $(docker image inspect --format '{{.Id}}' "${image}" | cut -c1-19)"
done

if [[ "$TAG" != local ]]; then
  docker image prune -f >/dev/null
  echo
  echo "已构建 :${TAG}（不滚动 :local、不重启生产）。演练环境："
  echo "  起：docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml up -d"
  echo "  入口 http://127.0.0.1:7864/ ；停：docker compose -p prism-lab down"
  exit 0
fi

# 一条命令把五只服务（core/console + 三只旁路）一起对齐到 :local。
docker compose up -d

docker image prune -f >/dev/null

echo
docker ps --filter 'name=workbuddy2api-ui-me' --format '{{.Names}}\t{{.Image}}\t{{.Status}}'
curl -fsS -o /dev/null -w 'healthz=%{http_code}\n' --max-time 10 http://127.0.0.1:7863/healthz
echo "回退：docker tag workbuddy2api-console:local-prev workbuddy2api-console:local && docker tag workbuddy2api-core:local-prev workbuddy2api-core:local && docker compose up -d"
