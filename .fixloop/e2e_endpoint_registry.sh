#!/usr/bin/env bash
# Endpoint registry local end-to-end gate.
#
#   ./.fixloop/e2e_endpoint_registry.sh            # rebuild image, then run
#   ./.fixloop/e2e_endpoint_registry.sh --no-build # assertions only, current image
#
# Production-shaped: MySQL 8.2 primary + ClickHouse log DB + containerised app
# + fake upstream. Exit 0 only when every assertion passes; each run appends one
# line to .fixloop/runs-endpoint-registry.ndjson.
set -uo pipefail
export LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8
cd "$(dirname "$0")/.."

LOCAL=deploy/local
BASE=http://127.0.0.1:39047
BUILD=1
[[ "${1:-}" == "--no-build" ]] && BUILD=0

PASS=0
FAILS=()

note() { printf '  ok   %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  FAIL %s: %s\n' "$1" "$2"; FAILS+=("$1: $2"); }

mysql_pw() { grep '^MYSQL_ROOT_PASSWORD=' "${LOCAL}/.env" | cut -d= -f2-; }
ch_pw() { grep '^CLICKHOUSE_PASSWORD=' "${LOCAL}/.env" | cut -d= -f2-; }

mqs() { docker exec apiduo-local-mysql mysql -uroot -p"$(mysql_pw)" -N -r apiduo_local -e "$1" 2>&1 | grep -v "Using a password"; }
chs() { docker exec apiduo-local-clickhouse clickhouse-client --password="$(ch_pw)" -d apiduo_local_logs -q "$1" 2>/dev/null; }

wait_ready() {
  for _ in $(seq 1 60); do
    if curl -s --noproxy '*' --max-time 3 "${BASE}/api/status" | grep -q '"success":true'; then return 0; fi
    sleep 2
  done
  return 1
}

# ---------------------------------------------------------------- 0. stack
if [[ ${BUILD} -eq 1 ]]; then
  echo "==> build and start (MySQL + ClickHouse + app + mock)"
  ( cd "${LOCAL}" && ./up.sh --mock >/tmp/e2e-up.log 2>&1 ) || { echo "FAIL up.sh"; tail -20 /tmp/e2e-up.log; exit 1; }
fi
wait_ready || { echo "FAIL app not ready"; docker logs --tail 20 apiduo-local-app; exit 1; }

# The fake upstream is a bind-mounted script and compose does not restart it on
# file change, so the running process may predate the file endpoints.
docker restart apiduo-local-mock >/dev/null 2>&1
sleep 2

TOKEN="$(mqs "select \`key\` from tokens where name='e2e-local' limit 1")"
[[ -n "${TOKEN}" ]] || { echo "FAIL missing e2e-local token"; exit 1; }

# ---------------------------------------------------------------- 1. seed
# A model-less endpoint enters routing through abilities, so the seed has to
# write both the channel and its capability rows.
mqs "
insert into channels (type,\`key\`,status,name,base_url,models,\`group\`,priority,weight,created_time,auto_ban)
select 43,'sk-mock',1,'mock-deepseek','http://mock-upstream:18080','deepseek-search,deepseek-fetch','default',0,0,unix_timestamp(),1
from dual where not exists (select 1 from channels where name='mock-deepseek');
update channels set models=concat(models,',@files/upload/file-extract')
  where id=1 and models not like '%@files/upload/file-extract%';
" >/dev/null

DEEPSEEK_ID="$(mqs "select id from channels where name='mock-deepseek' limit 1")"
mqs "
insert ignore into abilities (\`group\`,model,channel_id,enabled,priority,weight,tag)
select 'default','deepseek-search',${DEEPSEEK_ID},1,0,0,'' from dual;
insert ignore into abilities (\`group\`,model,channel_id,enabled,priority,weight,tag)
select 'default','deepseek-fetch',${DEEPSEEK_ID},1,0,0,'' from dual;
insert ignore into abilities (\`group\`,model,channel_id,enabled,priority,weight,tag)
select 'default','@files/upload/file-extract',1,1,0,0,'' from dual;
" >/dev/null

# ---------------------------------------------------------------- 2. migration
# The legacy channel declares purposes only; its model list has no routing name.
# The settings blob is written as hex so no layer of shell or SQL quoting can
# mangle it: an earlier version lost the object and the reset silently became
# "{}", which then looked like the migration had wiped the field.
LEGACY_SETTINGS_JSON='{"file_relay":{"purposes":["file-extract","batch"],"max_upload_bytes":1048576}}'
LEGACY_SETTINGS_HEX="$(printf '%s' "${LEGACY_SETTINGS_JSON}" | xxd -p | tr -d '\n')"
mqs "
insert into channels (type,\`key\`,status,name,base_url,models,\`group\`,priority,weight,created_time,auto_ban,settings)
select 1,'sk-mock',1,'mock-legacy-files','http://mock-upstream:18080','mock-gpt','default',0,0,unix_timestamp(),1,''
from dual where not exists (select 1 from channels where name='mock-legacy-files');
update channels set models='mock-gpt', settings=unhex('${LEGACY_SETTINGS_HEX}')
 where name='mock-legacy-files';
" >/dev/null

SEEDED="$(mqs "select settings from channels where name='mock-legacy-files' limit 1")"
[[ "${SEEDED}" == *'"purposes"'* ]] || { echo "FAIL seed: legacy settings not written (got=${SEEDED})"; exit 1; }

docker restart apiduo-local-app >/dev/null 2>&1
wait_ready || { echo "FAIL app not ready after restart"; exit 1; }

LEGACY_MODELS="$(mqs "select models from channels where name='mock-legacy-files' limit 1")"
LEGACY_SETTINGS="$(mqs "select settings from channels where name='mock-legacy-files' limit 1")"
LEGACY_ID="$(mqs "select id from channels where name='mock-legacy-files' limit 1")"

[[ "${LEGACY_MODELS}" == *"@files/upload/file-extract"* ]] \
  && note "migration: models gained @files/upload/file-extract" \
  || fail "migration-models" "got=${LEGACY_MODELS}"
[[ "${LEGACY_MODELS}" == *"@files/upload/batch"* ]] \
  && note "migration: models gained @files/upload/batch" \
  || fail "migration-batch" "got=${LEGACY_MODELS}"
[[ "${LEGACY_SETTINGS}" != *'"purposes"'* ]] \
  && note "migration: purposes field removed" \
  || fail "migration-residue" "got=${LEGACY_SETTINGS}"
[[ "${LEGACY_SETTINGS}" == *'"max_upload_bytes":1048576'* ]] \
  && note "migration: other settings preserved" \
  || fail "migration-settings" "got=${LEGACY_SETTINGS}"
[[ "$(mqs "select count(*) from abilities where model='@files/upload/batch' and channel_id=${LEGACY_ID}")" == "1" ]] \
  && note "migration: abilities rebuilt" \
  || fail "migration-ability" "missing @files/upload/batch row"

# ---------------------------------------------------------------- 3. relay
FIXTURE=/tmp/e2e-file-fixture.txt
printf 'hello-fixture-42' >"${FIXTURE}"

TOOLS_BODY="$(curl -s --noproxy '*' -X POST "${BASE}/v1/tools/search" \
  -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"query":"deepseek"}')"
[[ "${TOOLS_BODY}" == *'"results"'* ]] \
  && note "/v1/tools/search returns upstream results" \
  || fail "tools-search" "${TOOLS_BODY:0:160}"

TOOLS_LOG=0
for _ in $(seq 1 30); do
  TOOLS_LOG="$(chs "select count() from logs where model_name='deepseek-search' and quota>0")"
  [[ "${TOOLS_LOG:-0}" -ge 1 ]] && break
  sleep 1
done
[[ "${TOOLS_LOG:-0}" -ge 1 ]] \
  && note "/v1/tools/search wrote a billed consume log" \
  || fail "tools-search-log" "no deepseek-search consume row"

UPLOAD="$(curl -s --noproxy '*' -X POST "${BASE}/v1/files" \
  -H "Authorization: Bearer ${TOKEN}" \
  -F "file=@${FIXTURE};filename=notes.txt" -F 'purpose=file-extract')"
FILE_ID="$(printf '%s' "${UPLOAD}" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')"
[[ "${UPLOAD}" == *'"object":"file"'* && -n "${FILE_ID}" ]] \
  && note "/v1/files upload 200 (id=${FILE_ID})" \
  || fail "files-upload" "${UPLOAD:0:200}"

[[ "$(printf '%s' "${UPLOAD}" | sed -n 's/.*"bytes":\([0-9]*\).*/\1/p')" == "16" ]] \
  && note "/v1/files reports the byte count" \
  || fail "files-bytes" "${UPLOAD:0:200}"

CONTENT="$(curl -s --noproxy '*' "${BASE}/v1/files/${FILE_ID}/content" -H "Authorization: Bearer ${TOKEN}")"
[[ "${CONTENT}" == "hello-fixture-42" ]] \
  && note "/v1/files/:id/content replays the bytes by ChannelId" \
  || fail "files-content" "${CONTENT:0:120}"

DELETED="$(curl -s --noproxy '*' -X DELETE "${BASE}/v1/files/${FILE_ID}" -H "Authorization: Bearer ${TOKEN}")"
[[ "${DELETED}" == *'"deleted"'* || "${DELETED}" == *'"id"'* ]] \
  && note "/v1/files/:id delete accepted" \
  || fail "files-delete" "${DELETED:0:160}"

GONE="$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "${BASE}/v1/files/${FILE_ID}" -H "Authorization: Bearer ${TOKEN}")"
[[ "${GONE}" == "404" ]] \
  && note "deleted file answers 404" \
  || fail "files-gone" "http=${GONE}"

NOPURPOSE="$(curl -s --noproxy '*' -X POST "${BASE}/v1/files" -H "Authorization: Bearer ${TOKEN}" \
  -F "file=@${FIXTURE};filename=notes.txt" -F 'purpose=no-such-purpose')"
[[ "${NOPURPOSE}" == *'no_file_channel'* ]] \
  && note "unserved purpose answers 503 no_file_channel" \
  || fail "files-nopurpose" "${NOPURPOSE:0:160}"

# ---------------------------------------------------------------- 4. model list
MODELS="$(curl -s --noproxy '*' "${BASE}/v1/models" -H "Authorization: Bearer ${TOKEN}")"
[[ "${MODELS}" != *'@files/upload/'* ]] \
  && note "/v1/models hides @ routing names" \
  || fail "models-files" "list exposes @files/upload/"
[[ "${MODELS}" != *'deepseek-search'* ]] \
  && note "/v1/models hides deepseek-search" \
  || fail "models-tools" "list exposes deepseek-search"

# ---------------------------------------------------------------- 5. i18n
# The console hint is a new English source string, so every locale needs the
# key; without it the drawer silently falls back to English (caught by the bsk
# screenshot, not by any Go test).
I18N_MISSING=""
for lang in en zh zh-TW fr ru ja vi; do
  grep -q '@files/upload/<purpose>' "web/src/i18n/locales/${lang}.json" || I18N_MISSING="${I18N_MISSING} ${lang}"
done
[[ -z "${I18N_MISSING}" ]] \
  && note "i18n: file relay hint present in all 7 locales" \
  || fail "i18n-hint" "missing in${I18N_MISSING}"

# ---------------------------------------------------------------- summary
TOTAL=$((PASS + ${#FAILS[@]}))
echo
echo "checks ${TOTAL}, passed ${PASS}, failed ${#FAILS[@]}"
if [[ ${#FAILS[@]} -gt 0 ]]; then
  for item in ${FAILS[@]+"${FAILS[@]}"}; do echo "  - ${item}"; done
fi

mkdir -p .fixloop/logs
ROUND=$(cat .fixloop/runs-endpoint-registry.ndjson 2>/dev/null | wc -l | tr -d " ")
ROUND=$((ROUND + 1))
LOG=".fixloop/logs/endpoint-r$(printf '%02d' "${ROUND}").log"
{ echo "cmd: $0 $*"; echo "pass=${PASS} fail=${#FAILS[@]}"; printf '%s\n' ${FAILS[@]+"${FAILS[@]}"}; } >"${LOG}"
FIRST="${FAILS[0]:-}"
python3 - "${ROUND}" "${TOTAL}" "${#FAILS[@]}" "${FIRST}" "${LOG}" <<'PY' >>.fixloop/runs-endpoint-registry.ndjson
import json, re, sys, subprocess, datetime
round_no, total, failed, first, log = sys.argv[1:6]
head = subprocess.run(["git", "rev-parse", "--short", "HEAD"], capture_output=True, text=True).stdout.strip()
def norm(s):
    s = re.sub(r"0x[0-9a-f]+", "<HEX>", s)
    s = re.sub(r"/[^\s:]+/", "<PATH>/", s)
    return s.strip()[:160]
print(json.dumps({
    "round": int(round_no),
    "ts": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
    "cmd": ".fixloop/e2e_endpoint_registry.sh",
    "exit_code": 0 if failed == "0" else 1,
    "passed": int(total) - int(failed),
    "failed": int(failed),
    "fingerprint": norm(first) or "",
    "first_error": first[:200],
    "log_path": log,
    "git_head": head,
    "attribution": "" if failed == "0" else "IMPL",
    "decision": "PASS" if failed == "0" else "FIX",
}, ensure_ascii=False))
PY

[[ ${#FAILS[@]} -eq 0 ]] || exit 1
