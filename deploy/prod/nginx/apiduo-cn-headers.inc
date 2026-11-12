# 安全响应头 —— server 级 include，/static/ 的 location 里再 include 一次：
# nginx 的 add_header 不做父块继承（子块只要出现一条 add_header 就整体脱离父块），
# 所以凡是自带 add_header 的 location 都必须显式重复 include 本文件。
add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
add_header X-Content-Type-Options    "nosniff" always;
add_header Referrer-Policy           "strict-origin-when-cross-origin" always;
add_header X-Frame-Options           "SAMEORIGIN" always;
add_header Permissions-Policy        "camera=(), microphone=(), geolocation=()" always;

# 不给 CSP：new-api 前端产物含内联样式与运行时 <style> 注入，严格 script-src/style-src
# 会直接白屏；只写 report-only 又等于没设。宁缺勿滥。

# 新实例默认禁抓（空库上线阶段）。要对客做 SEO 时，删掉这一行与 -ssl.conf 的 robots.txt 段。
add_header X-Robots-Tag             "noindex, nofollow" always;
