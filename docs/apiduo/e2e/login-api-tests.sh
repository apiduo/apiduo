#!/usr/bin/env bash
# apiduo 后端接口端到端测试
#
#   bash docs/apiduo/e2e/login-api-tests.sh
#   BASE=http://127.0.0.1:3901 USER_NAME=root USER_PASS=LocalTest12345 bash ...
#
# 产物：docs/apiduo/e2e/artifacts/login/api/*.http（请求/响应原文，Cookie 值已脱敏）
# 退出码：有断言失败则为 1
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:3901}"
USER_NAME="${USER_NAME:-root}"
USER_PASS="${USER_PASS:-LocalTest12345}"
OUT="$(cd "$(dirname "$0")" && pwd)/artifacts/login/api"
mkdir -p "$OUT"
rm -f "$OUT"/*.http "$OUT"/session.jar

PASS=0
FAIL=0
STATUS=""
BODY=""
TOKEN=""
declare -a NOTES=()

red() { printf '\033[31m%s\033[0m' "$1"; }
grn() { printf '\033[32m%s\033[0m' "$1"; }

check() { # name expected actual
  local name="$1" expect="$2" actual="$3" shown="${3:0:150}"
  if [[ "$actual" == *"$expect"* ]]; then
    PASS=$((PASS + 1)); printf '  [%s] %s  · %s\n' "$(grn PASS)" "$name" "$shown"
  else
    FAIL=$((FAIL + 1)); printf '  [%s] %s  · 期望含 %s，实际 %s\n' "$(red FAIL)" "$name" "$expect" "$shown"
  fi
}

note() { NOTES+=("$1"); printf '  [INFO] %s\n' "$1"; }

J() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval(\"d$1\"))" 2>/dev/null || echo "__ERR__"; }

# req NAME METHOD PATH [JSON_BODY] [AUTH: bearer|jar|none]
req() {
  local name="$1" method="$2" path="$3" body="${4:-}" auth="${5:-none}"
  local -a args=(-sS --noproxy '*' -D "$OUT/$name.h.tmp" -o "$OUT/$name.b.tmp" -w '%{http_code}'
    -X "$method" "$BASE$path" -H 'Content-Type: application/json')
  [[ -n "$body" ]] && args+=(-d "$body")
  [[ "$auth" == "bearer" && -n "$TOKEN" ]] && args+=(-H "Authorization: Bearer $TOKEN")
  [[ "$auth" == "jar" ]] && args+=(-b "$OUT/session.jar")
  STATUS=$(curl "${args[@]}" 2>"$OUT/$name.err")
  BODY=$(cat "$OUT/$name.b.tmp" 2>/dev/null)
  # 落盘可复核的报文（值脱敏，属性保留）
  {
    printf '%s %s HTTP/1.1\n' "$method" "$path"
    printf 'Host: %s\n' "${BASE#http://}"
    [[ -n "$body" ]] && printf 'Content-Type: application/json\n' && printf '\n%s\n' "$body"
    printf '\n--- response %s ---\n' "$STATUS"
    sed -E 's/(new_api_refresh|new_api_has_session|set-cookie: *[^=]+)=([^;]*)/\1=<redacted>/Ig' "$OUT/$name.h.tmp"
    printf '\n%s\n' "$BODY"
  } >"$OUT/$name.http"
  rm -f "$OUT/$name.h.tmp" "$OUT/$name.b.tmp" "$OUT/$name.err"
}

echo
echo "T1 匿名读取站点状态"
req t1-status GET /api/status
st=$STATUS
check "GET /api/status 返回 200" "200" "$st"
theme=$(printf '%s' "$BODY" | J "['data']['login_theme']")
check "login_theme 字段存在" "aurora" "$theme"
note "system_name=$(printf '%s' "$BODY" | J "['data']['system_name']") version=$(printf '%s' "$BODY" | J "['data']['version']")"

echo
echo "T2 未授权写配置"
req t2-put-anon PUT /api/option/ '{"key":"LoginTheme","value":"cyber"}'
st=$STATUS
check "匿名 PUT /api/option 返回 401" "401" "$st"

echo
echo "T3 错误口令"
req t3-login-bad POST /api/user/login '{"username":"'"$USER_NAME"'","password":"definitely-wrong"}'
st=$STATUS
check "错误口令 HTTP 200（业务失败走 body）" "200" "$st"
check "success=false" "False" "$(printf '%s' "$BODY" | J "['success']")"
check "错误信息不泄露账号是否存在" "Username or password is incorrect" "$(printf '%s' "$BODY" | J "['message']")"

echo
echo "T4 正确口令与会话"
req t4-login-ok POST /api/user/login '{"username":"'"$USER_NAME"'","password":"'"$USER_PASS"'"}'
st=$STATUS
check "登录返回 200" "200" "$st"
check "success=true" "True" "$(printf '%s' "$BODY" | J "['success']")"
TOKEN=$(printf '%s' "$BODY" | J "['data']['access_token']")
if [[ ${#TOKEN} -gt 20 ]]; then
  PASS=$((PASS + 1)); printf '  [%s] access_token 已下发  · 长度 %s\n' "$(grn PASS)" "${#TOKEN}"
else
  FAIL=$((FAIL + 1)); printf '  [%s] access_token 已下发  · 实际 %s\n' "$(red FAIL)" "$TOKEN"
fi
cookies=$(sed -nE 's/^[Ss]et-[Cc]ookie: *//p' "$OUT/t4-login-ok.http" | tr '\n' '|')
check "下发刷新 Cookie 且 HttpOnly" "HttpOnly" "$cookies"
check "刷新 Cookie SameSite=Strict" "SameSite=Strict" "$cookies"
check "刷新 Cookie 限定 /api/user/auth 路径" "Path=/api/user/auth" "$cookies"

echo
echo "T5 鉴权读取配置"
req t5-option-list GET /api/option/ '' bearer
st=$STATUS
check "带 Bearer 读取 /api/option 返回 200" "200" "$st"
check "配置列表包含 LoginTheme" "LoginTheme" "$BODY"

echo
echo "T6 写配置并落到 status"
req t6-put-theme PUT /api/option/ '{"key":"LoginTheme","value":"warm"}' bearer
st=$STATUS
check "写入 LoginTheme=warm 返回 200" "200" "$st"
check "写入 success=true" "True" "$(printf '%s' "$BODY" | J "['success']")"
req t6b-status GET /api/status
st=$STATUS
check "匿名 status 立即反映 warm" "warm" "$(printf '%s' "$BODY" | J "['data']['login_theme']")"

echo
echo "T7 仅 Cookie 不能改配置（CSRF 面）"
req t7-put-cookie PUT /api/option/ '{"key":"LoginTheme","value":"terminal"}' jar
st=$STATUS
check "仅会话 Cookie 的 PUT 被拒（401）" "401" "$st"
req t7b-status GET /api/status
st=$STATUS
check "被拒后配置未被改动" "warm" "$(printf '%s' "$BODY" | J "['data']['login_theme']")"

echo
echo "T8 刷新令牌链路"
# 用真实登录写 cookie jar
curl -sS --noproxy '*' -c "$OUT/session.jar" -o /dev/null -X POST "$BASE/api/user/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"'"$USER_NAME"'","password":"'"$USER_PASS"'"}' >/dev/null
req t8-refresh POST /api/user/auth/refresh '' jar
st=$STATUS
check "带刷新 Cookie 换发令牌返回 200" "200" "$st"
rt=$(printf '%s' "$BODY" | J "['data']['access_token']")
if [[ ${#rt} -gt 20 ]]; then
  PASS=$((PASS + 1)); printf '  [%s] refresh 返回新的 access_token  · 长度 %s\n' "$(grn PASS)" "${#rt}"
else
  FAIL=$((FAIL + 1)); printf '  [%s] refresh 返回新的 access_token  · 实际 %s\n' "$(red FAIL)" "$rt"
fi
req t8b-refresh-anon POST /api/user/auth/refresh ''
st=$STATUS
check "无 Cookie 的 refresh 返回 401（登录页那条红字来源）" "401" "$st"

echo
echo "T9 越界输入（只记录，不改配置）"
req t9-put-invalid PUT /api/option/ '{"key":"LoginTheme","value":"<script>alert(1)</script>"}' bearer
st=$STATUS
bad="$(printf '%s' "$BODY" | J "['success']")"
note "非法主题值写入：HTTP $st success=$bad"
req t9b-status GET /api/status
st=$STATUS
raw=$(printf '%s' "$BODY" | J "['data']['login_theme']")
note "status 原样返回该值：${raw:0:40}"
note "前端对未知值回落 aurora（已由 S3 验证），因此无注入风险，但服务端未做白名单校验"
req t9c-put-unknown PUT /api/option/ '{"key":"NotAnOption","value":"1"}' bearer
st=$STATUS
note "未知 option 写入：HTTP $st success=$(printf '%s' "$BODY" | J "['success']")（上游行为，未拦截）"

echo
echo "T10 恢复默认"
req t10-restore PUT /api/option/ '{"key":"LoginTheme","value":"aurora"}' bearer
st=$STATUS
check "恢复 aurora 返回 200" "200" "$st"
req t10b-status GET /api/status
st=$STATUS
check "最终 login_theme=aurora" "aurora" "$(printf '%s' "$BODY" | J "['data']['login_theme']")"

echo
echo "────────────────────────────────────────"
printf '断言 %s 通过 / %s 失败；观察 %s 条\n' "$PASS" "$FAIL" "${#NOTES[@]}"
echo "报文：$OUT/*.http"
[[ "$FAIL" -eq 0 ]] || exit 1
