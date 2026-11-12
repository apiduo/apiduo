# apiduo.cn 的公共反代指令 —— 被 apiduo.cn-ssl.conf 的 location 引用。
# 后端固定回环 127.0.0.1:39047（新仓实例）；旧站是 39045，互不干扰。
proxy_pass http://127.0.0.1:39047;
proxy_http_version 1.1;
# 🔴 Host 不能省：Go 靠 request.Host 决定 cookie 落哪个域，
#    middleware/auth_origin.go 的 isAllowedSessionOrigin 也拿它比对 Origin。
proxy_set_header Host              $host;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;

# ⚠️ Connection 必须显式清空，否则回源无法 keepalive。
#    SSE 是普通 HTTP 长连接，不需要 Upgrade 语义；别写 $connection_upgrade
#    （需在 http 块 map 定义，宝塔主配置未必有 ⇒ nginx -t 直接失败）。
proxy_set_header Connection        "";

# 绘图参考图 multipart 上传，nginx 默认 1m 会直接 413。
client_max_body_size 32m;

# 对话/绘图长连接（SSE 与长轮询），不能按默认 60s 掐断。
# ⚠️ proxy_buffering 不在这里设：/static/ 要 on、其余要 off，
#    写进公共片段会与 location 里的指令重复（nginx 报 duplicate）。
proxy_read_timeout 300s;
proxy_send_timeout 300s;
