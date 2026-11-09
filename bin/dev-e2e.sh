#!/usr/bin/env bash
# apiduo 本地实例：起真网关 + 假上游，按需跑端到端套件。
#
#   bin/dev-e2e.sh                    起服务并前台守着（Ctrl-C 退出，自动清理）
#   bin/dev-e2e.sh --all              起服务 + 跑全部单实例套件，跑完清理
#   bin/dev-e2e.sh --suites a,b       只跑指定套件（逗号分隔）
#   bin/dev-e2e.sh --all --keep       跑完不清理，留现场手点
#
# 和 e2e/live/run_all.sh 的分工：
#   run_all.sh  = 一次性 CI 回归，3 个实例（trace / image / off）+ 临时库，跑完即焚
#   dev-e2e.sh  = 开发者常驻环境，单实例 + 持久数据目录，重启不丢账号和渠道
#
# 数据目录里保留：apiduo/fakes 二进制、SQLite、假上游抓到的报文、各进程日志。
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/.." && pwd)

GW_PORT=${GW_PORT:-39300}
UP_PORT=${UP_PORT:-39401}
UP2_PORT=${UP2_PORT:-39404}
IMG_PORT=${IMG_PORT:-39403}
SINK_PORT=$((UP_PORT + 1))
SINK2_PORT=$((UP2_PORT + 1))

DATA=${DATA:-/tmp/apiduo-dev-e2e}
ADMIN_USER=${ADMIN_USER:-root}
ADMIN_PW=${ADMIN_PW:-E2ePassw0rd!2026}
BASE="http://127.0.0.1:$GW_PORT"

ALL_SUITES=(trace privacy select affinity health multikey autogroup override combo discount)
SUITES=""
KEEP=0
REBUILD=0
RELAY_KEY=""
pids=()

usage() {
  sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'EOF'

可选环境变量（括号内为默认值）
  GW_PORT (39300)   UP_PORT (39401)   UP2_PORT (39404)   IMG_PORT (39403)
  DATA (/tmp/apiduo-dev-e2e)         ADMIN_USER (root)  ADMIN_PW (E2ePassw0rd!2026)

套件名：trace privacy select affinity health multikey autogroup override combo discount
（off / image 依赖各自专用实例，走 e2e/live/run_all.sh）
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --all) SUITES="${ALL_SUITES[*]}"; shift ;;
    --suites) SUITES=${2//,/ }; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --rebuild) REBUILD=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1（-h 看用法）" >&2; exit 2 ;;
  esac
done

# ---------------------------------------------------------------- 进程与端口

cleanup() {
  if [ "$KEEP" = 1 ]; then
    echo "已保留进程与数据：$DATA"
    return 0
  fi
  for pid in "${pids[@]:-}"; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
  done
  # 兜底按端口：管道被中断时子进程会脱离 pids。一律 -9，这类常驻进程不吃 SIGTERM。
  for port in "$GW_PORT" "$UP_PORT" "$SINK_PORT" "$UP2_PORT" "$SINK2_PORT" "$IMG_PORT"; do
    local orphans
    orphans=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)
    [ -n "$orphans" ] && kill -9 $orphans 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

# 这些端口归本脚本用。占着它们且 cmdline 是自己的上一轮残留，直接回收；
# 是别人的服务就报错退出，不替用户做决定。
reclaim_port() {
  local port=$1 found pid cmd
  found=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)
  [ -z "$found" ] && return 0
  for pid in $found; do
    cmd=$(ps -o command= -p "$pid" 2>/dev/null || true)
    case "$cmd" in
      *apiduo-dev-e2e*|*apiduo-local-run*|*image_mock.py*)
        kill -9 "$pid" 2>/dev/null || true
        echo "  回收上一轮残留：port=$port pid=$pid"
        ;;
      *)
        echo "端口 $port 已被别的进程占用：${cmd:-pid $pid}" >&2
        echo "换端口（GW_PORT=... ）或先停掉它" >&2
        exit 1
        ;;
    esac
  done
  sleep 1
}

# ---------------------------------------------------------------- 构建

rebuild_web() {
  # rsbuild 收尾会 fs.cp 覆盖 public/ 里的同名文件，旧产物还在就撞沙箱
  # safe-delete 的批量删除闸门（累计 64 个即失败），报错却写成
  # "Failed to copy public directory"。先清空，纯新建就不删任何东西。
  find "$repo/web/dist" -mindepth 1 -delete 2>/dev/null || true
  (cd "$repo/web" && bun run build)
}

build_all() {
  if [ ! -f "$repo/web/dist/index.html" ]; then
    echo "== 构建前端 =="
    rebuild_web
  fi

  local stale=""
  if [ -x "$DATA/apiduo" ]; then
    stale=$(find "$repo" -name '*.go' -not -path '*/node_modules/*' -newer "$DATA/apiduo" -print -quit 2>/dev/null || true)
  fi
  if [ "$REBUILD" = 1 ] || [ ! -x "$DATA/apiduo" ] || [ -n "$stale" ]; then
    echo "== 构建网关${stale:+（$stale 有更新）} =="
    (cd "$repo" && go build -o "$DATA/apiduo" .)
  fi
  if [ "$REBUILD" = 1 ] || [ ! -x "$DATA/fakes" ]; then
    (cd "$repo/e2e/live/fakes" && go build -o "$DATA/fakes" .)
  fi
}

# ---------------------------------------------------------------- 起服务

start_fakes() {
  "$DATA/fakes" -upstream "127.0.0.1:$UP_PORT" -sink "127.0.0.1:$SINK_PORT" \
    -dir "$DATA/sink" -headers "$DATA/logs/upstream-headers.log" \
    -bodies "$DATA/logs/upstream-bodies.log" >"$DATA/logs/fakes.log" 2>&1 &
  pids+=($!)

  # 第二台：多渠道选择/亲和要能分辨「这一请求被送到哪台」
  "$DATA/fakes" -upstream "127.0.0.1:$UP2_PORT" -sink "127.0.0.1:$SINK2_PORT" \
    -dir "$DATA/sink2" -headers "$DATA/logs/upstream-headers-2.log" \
    -bodies "$DATA/logs/upstream-bodies-2.log" -fail-prefix= >"$DATA/logs/fakes2.log" 2>&1 &
  pids+=($!)

  # 图片协议 / 折扣套件要它
  python3 "$repo/e2e/live/fakes/image_mock.py" --port "$IMG_PORT" \
    --log "$DATA/logs/image.log" >"$DATA/logs/image-mock.log" 2>&1 &
  pids+=($!)
}

start_gateway() {
  # LANGFUSE 必须开：privacy 套件要断言「管理员视图带 trace_id」
  (
    cd "$DATA/gw" && exec env \
      PORT="$GW_PORT" GIN_MODE=release SYNC_FREQUENCY=1 \
      MEMORY_CACHE_ENABLED=true ERROR_LOG_ENABLED=true \
      CRITICAL_RATE_LIMIT=5000 CRITICAL_RATE_LIMIT_DURATION=1800 \
      GLOBAL_API_RATE_LIMIT=200000 GLOBAL_WEB_RATE_LIMIT=200000 \
      LANGFUSE_ENABLED=true LANGFUSE_HOST="http://127.0.0.1:$SINK_PORT" \
      LANGFUSE_CONSOLE_URL="http://127.0.0.1:$SINK_PORT" \
      LANGFUSE_PUBLIC_KEY=pk-lf-dev LANGFUSE_SECRET_KEY=sk-lf-dev \
      LANGFUSE_PROJECT_ID=dev-project LANGFUSE_BODY_MODE=headtail \
      LANGFUSE_SAMPLE_RATE=100 LANGFUSE_ERROR_SAMPLE_RATE=100 LANGFUSE_FLUSH_INTERVAL_MS=200 \
      "$DATA/apiduo"
  ) >"$DATA/logs/gateway.log" 2>&1 &
  pids+=($!)
}

wait_ready() {
  local i
  for i in $(seq 1 60); do
    if curl -s --noproxy '*' -o /dev/null "$BASE/api/status" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "网关 30s 内没起来，看 $DATA/logs/gateway.log" >&2
  tail -5 "$DATA/logs/gateway.log" >&2 || true
  return 1
}

# 初始化（幂等：已有库沿用，同名渠道删了重建，令牌复用）
bootstrap() {
  E2E_BASE="$BASE" E2E_UPSTREAM="http://127.0.0.1:$UP_PORT" \
  E2E_ADMIN="$ADMIN_USER" E2E_ADMIN_PW="$ADMIN_PW" \
  E2E_CHANNEL=dev-openai E2E_MODEL=gpt-4o-mini E2E_SELF_USE=true \
    python3 "$repo/e2e/live/lib/bootstrap.py" 2>/dev/null | tail -1
}

# ---------------------------------------------------------------- 跑套件

suite_file() {
  case "$1" in
    trace) echo trace.py ;;
    privacy) echo privacy.py ;;
    select) echo channel_select.py ;;
    affinity) echo channel_affinity.py ;;
    health) echo channel_health.py ;;
    multikey) echo multi_key_sticky.py ;;
    autogroup) echo auto_group.py ;;
    override) echo affinity_override.py ;;
    combo) echo combo_group_multikey.py ;;
    discount) echo discount_pat.py ;;
    *) return 1 ;;
  esac
}

# 套件从环境里读的默认值都是 run_all.sh 的口径，手跑必须逐个覆盖，否则假红。
suite_env() {
  export E2E_BASE="$BASE" E2E_RELAY_KEY="$RELAY_KEY"
  export E2E_UP1="http://127.0.0.1:$UP_PORT" E2E_UP2="http://127.0.0.1:$UP2_PORT"
  export E2E_H1="$DATA/logs/upstream-headers.log" E2E_H2="$DATA/logs/upstream-headers-2.log"
  export E2E_BODIES="$DATA/logs/upstream-bodies.log" E2E_IMAGE_LOG="$DATA/logs/image.log"
  export E2E_SINK="http://127.0.0.1:$SINK_PORT" E2E_SINK_DIR="$DATA/sink"
  export E2E_PROJECT_ID=dev-project E2E_CONSOLE="http://127.0.0.1:$SINK_PORT"
  # 这两条的默认值写死在套件里（39402 / sk-lf-e2e），端口一改就必须覆盖
  export E2E_HOST_HINT="$SINK_PORT" E2E_SECRET_KEY=sk-lf-dev
  export E2E_ADMIN="$ADMIN_USER" E2E_ADMIN_PW="$ADMIN_PW"
}

restore_self_use() {
  # discount_pat 会关掉自用模式并只给 disc-model 配价；跑完恢复，
  # 否则这个实例接着手点会撞 "price not configured"。
  local jwt
  jwt=$(curl -s --noproxy '*' -X POST "$BASE/api/user/login" -H 'Content-Type: application/json' \
        -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PW\"}" \
        | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["access_token"])' 2>/dev/null || true)
  [ -z "$jwt" ] && return 0
  curl -s --noproxy '*' -X PUT "$BASE/api/option/" -H "Authorization: Bearer $jwt" \
    -H 'Content-Type: application/json' -d '{"key":"SelfUseModeEnabled","value":"true"}' >/dev/null || true
}

run_suites() {
  local s f passed=0 failed=0 failed_names=()
  for s in $SUITES; do
    if ! f=$(suite_file "$s"); then
      echo "未知套件：$s（-h 看清单）" >&2
      failed=$((failed + 1))
      failed_names+=("$s: 未知")
      continue
    fi
    echo
    echo "== $s =="
    if [ "$s" = trace ]; then
      # trace 会遍历 sink 目录里**所有** span 文件做交叉核对。数据目录是持久的，
      # 上一轮的 span 混进来就会报「日志行指向错的 trace」——那不是产品问题。
      find "$DATA/sink" -mindepth 1 -delete 2>/dev/null || true
    fi
    local rc=0
    if [ "$s" = discount ]; then
      # 这一套的「上游」是图片 mock，不是 E2E_UP1
      E2E_UPSTREAM="http://127.0.0.1:$IMG_PORT" python3 "$repo/e2e/live/suites/$f" || rc=$?
    else
      python3 "$repo/e2e/live/suites/$f" || rc=$?
    fi
    if [ "$rc" = 0 ]; then
      passed=$((passed + 1))
    else
      failed=$((failed + 1))
      failed_names+=("$s")
    fi
  done

  restore_self_use

  echo
  echo "==== 汇总：$passed 套通过 / $failed 套失败 ===="
  if [ "$failed" -ne 0 ]; then
    printf '  失败：%s\n' "${failed_names[@]}"
    return 1
  fi
}

# ---------------------------------------------------------------- 主流程

echo "== 准备数据目录 $DATA =="
mkdir -p "$DATA"/{gw,logs,sink,sink2}
: >"$DATA/logs/upstream-headers.log"
: >"$DATA/logs/upstream-headers-2.log"
: >"$DATA/logs/upstream-bodies.log"
: >"$DATA/logs/upstream-bodies-2.log"
: >"$DATA/logs/image.log"

for port in "$GW_PORT" "$UP_PORT" "$SINK_PORT" "$UP2_PORT" "$SINK2_PORT" "$IMG_PORT"; do
  reclaim_port "$port"
done

build_all

echo "== 起服务 =="
start_fakes
start_gateway
wait_ready

RELAY_KEY=$(bootstrap)
if [ -z "$RELAY_KEY" ]; then
  echo "bootstrap 没拿到中继 key，看 $DATA/logs/gateway.log" >&2
  exit 1
fi

echo
echo "  网关    $BASE"
echo "  账号    $ADMIN_USER / $ADMIN_PW"
echo "  中继    $RELAY_KEY"
echo "  上游    ${UP_PORT}（兼假 Langfuse sink ${SINK_PORT}） / ${UP2_PORT} / 图片 ${IMG_PORT}"
echo "  日志    $DATA/logs/"
echo

if [ -n "$SUITES" ]; then
  suite_env
  run_suites
else
  echo "前台守着，Ctrl-C 退出并清理。"
  while true; do sleep 30; done
fi
