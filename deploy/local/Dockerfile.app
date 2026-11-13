# 本地测试用镜像
#
# 生产镜像：根目录 Dockerfile，debian:bookworm-slim 运行态，构建期 apt-get 装
#           ca-certificates / tzdata / wget。
# 本地：沿用 Debian bookworm-slim 血统的 python:3.12-slim（本机已有），避免构建期
#       走外网 apt；Go 二进制是 CGO_ENABLED=0 静态链接，与运行态无关。
#       健康检查因此改用 python 实现（判据与生产一致：看 /api/status 的 success 字段）。
FROM python:3.12-slim

COPY bin/apiduo-server-linux-arm64 /new-api
COPY LICENSE NOTICE THIRD-PARTY-LICENSES.md /licenses/

EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/new-api"]
