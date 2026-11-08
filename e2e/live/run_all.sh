#!/usr/bin/env bash
# 一键跑完 apiduo 的端到端回归：起 3 个真网关实例 + 2 个假上游 + 1 个假 Langfuse sink。
#
# 设计原则：每个套件都跑在**当前源码构建的二进制**上，断言的是上游实际收到什么、
# sink 实际收到什么，而不是网关自己的日志。跑完自动回收，不留容器/进程/临时库。
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
run=$(mktemp -d "${TMPDIR:-/tmp}/apiduo-e2e.XXXXXX")

TRACE_UP=${TRACE_UP:-39401}
SINK=${SINK:-39402}
IMG_UP=${IMG_UP:-39403}
UP2=${UP2:-39404}
SINK2=${SINK2:-39405}
GW_TRACE=${GW_TRACE:-39410}
GW_IMAGE=${GW_IMAGE:-39420}
GW_OFF=${GW_OFF:-39440}

export E2E_SINK="http://127.0.0.1:$SINK"
export E2E_HEADERS="$run/upstream-headers.log"
export E2E_HEADERS2="$run/upstream-headers-2.log"
export E2E_UP1="http://127.0.0.1:$TRACE_UP"
export E2E_UP2="http://127.0.0.1:$UP2"
export E2E_H1="$E2E_HEADERS"
export E2E_H2="$E2E_HEADERS2"
export E2E_SINK_DIR="$run/sink"
export E2E_IMAGE_LOG="$run/image.log"
export E2E_PROJECT_ID="e2e-project"
export E2E_CONSOLE="http://127.0.0.1:$SINK"

pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  # 兜底：管道被中断（SIGPIPE）时子进程会漏掉，按端口再收一次。
  # 这些端口在启动前已确认空闲，所以监听者一定是本轮起的。
  for port in "$TRACE_UP" "$SINK" "$IMG_UP" "$UP2" "$SINK2" "$GW_TRACE" "$GW_IMAGE" "$GW_OFF"; do
    orphans=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)
    [ -n "$orphans" ] && kill $orphans 2>/dev/null || true
  done
  # 兜底：管道被中断（SIGPIPE）时子进程会漏掉，按端口再收一次。
  # 这些端口在启动前已确认空闲，所以监听者一定是本轮起的。
  for port in "$TRACE_UP" "$SINK" "$IMG_UP" "$UP2" "$SINK2" "$GW_TRACE" "$GW_IMAGE" "$GW_OFF"; do
    orphans=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)
    [ -n "$orphans" ] && kill $orphans 2>/dev/null || true
  done
  if [ "${KEEP:-0}" = "1" ]; then
    echo "已保留运行目录：$run"
  else
    rm -rf "$run"
  fi
}
trap cleanup EXIT

need_port() {
  if lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "端口 $1 已被占用（$(lsof -nP -iTCP:"$1" -sTCP:LISTEN -t | head -1)）：换一个，或先停掉旧实例" >&2
    exit 1
  fi
}
for p in "$TRACE_UP" "$SINK" "$IMG_UP" "$UP2" "$SINK2" "$GW_TRACE" "$GW_IMAGE" "$GW_OFF"; do need_port "$p"; done

echo "== 构建 =="
if [ ! -f "$repo/web/dist/index.html" ]; then
  echo "  web/dist 缺失或损坏，先构建前端（约 60s）"
  (cd "$repo/web" && bun run build)
fi
build_gateway() {
  build_gateway() {
  (cd "$repo" && go build -o "$run/apiduo" .) && return 0
  # 并行开发时别的进程可能在重写 web/dist，embed 会拿到半成品。
  # 这不是代码问题，重建前端再试一次即可。
  echo "  go build 失败（多半是 web/dist 正被重写），重建前端后重试" >&2
  (cd "$repo/web" && bun run build)
  (cd "$repo" && go build -o "$run/apiduo" .)
}
build_gateway && return 0
  # 并行开发时别的进程可能在重写 web/dist，embed 会拿到半成品。
  # 这不是代码问题，重建前端再试一次即可。
  echo "  go build 失败（多半是 web/dist 正被重写），重建前端后重试" >&2
  (cd "$repo/web" && bun run build)
  build_gateway() {
  (cd "$repo" && go build -o "$run/apiduo" .) && return 0
  # 并行开发时别的进程可能在重写 web/dist，embed 会拿到半成品。
  # 这不是代码问题，重建前端再试一次即可。
  echo "  go build 失败（多半是 web/dist 正被重写），重建前端后重试" >&2
  (cd "$repo/web" && bun run build)
  (cd "$repo" && go build -o "$run/apiduo" .)
}
build_gateway
}
build_gateway
(cd "$here/fakes" && go build -o "$run/fakes" .)

echo "== 起假服务 =="
mkdir -p "$run/sink" "$run/sink2" "$run/trace" "$run/image" "$run/off"
: > "$E2E_HEADERS"
: > "$E2E_HEADERS2"
: > "$E2E_IMAGE_LOG"
"$run/fakes" -upstream "127.0.0.1:$TRACE_UP" -sink "127.0.0.1:$SINK" -dir "$run/sink" -headers "$E2E_HEADERS" \
  >"$run/fakes.log" 2>&1 &
pids+=($!)
disown "$!" 2>/dev/null || true
# 第二台假上游：多渠道选择用例要能分辨「这一请求被送到了哪台」
"$run/fakes" -upstream "127.0.0.1:$UP2" -sink "127.0.0.1:$SINK2" -dir "$run/sink2" -headers "$E2E_HEADERS2" \
  >"$run/fakes2.log" 2>&1 &
pids+=($!)
disown "$!" 2>/dev/null || true
python3 "$here/fakes/image_mock.py" --port "$IMG_UP" --log "$E2E_IMAGE_LOG" >"$run/image-mock.log" 2>&1 &
pids+=($!)
disown "$!" 2>/dev/null || true

gw_env() {
  echo "PORT=$1 GIN_MODE=release SYNC_FREQUENCY=1 MEMORY_CACHE_ENABLED=true ERROR_LOG_ENABLED=true CRITICAL_RATE_LIMIT=5000 CRITICAL_RATE_LIMIT_DURATION=1800"
}

echo "== 起网关实例 =="
(cd "$run/trace" && env $(gw_env "$GW_TRACE") \
  LANGFUSE_ENABLED=true LANGFUSE_HOST="http://127.0.0.1:$SINK" LANGFUSE_CONSOLE_URL="http://127.0.0.1:$SINK" \
  LANGFUSE_PUBLIC_KEY=pk-lf-e2e LANGFUSE_SECRET_KEY=sk-lf-e2e LANGFUSE_PROJECT_ID="$E2E_PROJECT_ID" \
  LANGFUSE_BODY_MODE=headtail LANGFUSE_SAMPLE_RATE=100 LANGFUSE_ERROR_SAMPLE_RATE=100 LANGFUSE_FLUSH_INTERVAL_MS=200 \
  "$run/apiduo" >"$run/trace-gw.log" 2>&1) &
pids+=($!)
disown "$!" 2>/dev/null || true

(cd "$run/image" && env $(gw_env "$GW_IMAGE") LANGFUSE_ENABLED=false \
  "$run/apiduo" >"$run/image-gw.log" 2>&1) &
pids+=($!)
disown "$!" 2>/dev/null || true

(cd "$run/off" && env $(gw_env "$GW_OFF") LANGFUSE_ENABLED=false \
  "$run/apiduo" >"$run/off-gw.log" 2>&1) &
pids+=($!)
disown "$!" 2>/dev/null || true

bootstrap() {
  E2E_BASE=$1 E2E_UPSTREAM=$2 E2E_ADMIN=$3 E2E_ADMIN_PW=$4 E2E_CHANNEL=$5 E2E_MODEL=$6 \
  E2E_SELF_USE=$7 python3 "$here/lib/bootstrap.py" 2>/dev/null | tail -1
}

echo "== 初始化实例 =="
bootstrap "http://127.0.0.1:$GW_TRACE" "http://127.0.0.1:$TRACE_UP" root 'E2ePassw0rd!2026' e2e-openai gpt-4o-mini false >/dev/null
bootstrap "http://127.0.0.1:$GW_OFF" "http://127.0.0.1:$TRACE_UP" root 'E2ePassw0rd!2026' e2e-openai gpt-4o-mini false >/dev/null
export E2E_RELAY_KEY
E2E_RELAY_KEY=$(bootstrap "http://127.0.0.1:$GW_IMAGE" "http://127.0.0.1:$IMG_UP" admin 'apiduo-test-2026' e2e-img e2e-img true)
echo "  image 实例中继 key: ${E2E_RELAY_KEY:0:6}…"

suite() {
  local slug=$1 name=$2 base=$3 script=$4
  if [ -n "${ONLY:-}" ] && [ "$ONLY" != "$slug" ]; then return 0; fi
  echo
  echo "== $name =="
  if E2E_BASE="$base" python3 "$here/suites/$script"; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    failed_names+=("$name")
  fi
}

passed=0
failed=0
failed_names=()

suite trace "trace 端到端" "http://127.0.0.1:$GW_TRACE" trace.py
suite off "关闭态等价" "http://127.0.0.1:$GW_OFF" off.py
suite privacy "普通用户隐私" "http://127.0.0.1:$GW_TRACE" privacy.py
suite image "图片协议 + 主端点" "http://127.0.0.1:$GW_IMAGE" image_primary.py
# 顺序有意义：discount 会关掉自用模式并只给 disc-model 配价，
# 之后再跑 select 就会撞「price not configured」。select 必须排在它前面。
suite select "渠道选择/权重/降级" "http://127.0.0.1:$GW_IMAGE" channel_select.py
suite affinity "渠道亲和（sticky）" "http://127.0.0.1:$GW_IMAGE" channel_affinity.py
suite health "渠道健康（自动禁用/启用）" "http://127.0.0.1:$GW_IMAGE" channel_health.py
suite discount "折扣 + PAT + 审计" "http://127.0.0.1:$GW_IMAGE" discount_pat.py

echo
if [ -z "${ONLY:-}" ] || [ "$ONLY" = "ui" ]; then
echo "== 浏览器：日志页跳转 =="
if E2E_BASE="http://127.0.0.1:$GW_TRACE" node "$here/ui/trace_link.mjs"; then
  passed=$((passed + 1))
else
  failed=$((failed + 1))
  failed_names+=("ui/trace_link")
fi
fi

echo
echo "==== 汇总：$passed 套通过 / $failed 套失败 ===="
if [ "$failed" -ne 0 ]; then
  printf '  失败：%s\n' "${failed_names[@]}"
  exit 1
fi
