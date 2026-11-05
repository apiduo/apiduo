package middleware

import (
	"log"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ConfigureTrustedProxies(engine *gin.Engine) error {
	trustedProxies, usedDefaults, err := common.ResolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return err
	}
	if usedDefaults {
		log.Print(common.LogText("WARNING: TRUSTED_PROXIES is unset or blank; trusting loopback, RFC 1918, and IPv6 ULA proxy addresses for compatibility. Set TRUSTED_PROXIES=none to trust no proxies, or configure explicit proxy IPs/CIDRs to replace these defaults."))
	}
	return common.ConfigureTrustedProxies(engine, trustedProxies)
}

// APIDUO:token-ip-allowlist:BEGIN
//
// WarnIfIpPinNeedsProxyTrust reports a setting that looks enforced but is not:
// access tokens pinned to specific addresses, while every private range is
// still trusted as a proxy. Under that combination any host on the same
// network can set X-Forwarded-For and appear to come from an allowed address.
//
// It runs after the database is ready, because answering it needs a query.
func WarnIfIpPinNeedsProxyTrust() {
	_, usedDefaults, err := common.ResolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil || !usedDefaults || model.DB == nil {
		return
	}

	var pinned int64
	err = model.DB.Model(&model.UserAccessToken{}).
		Where("allow_ips IS NOT NULL AND TRIM(allow_ips) <> ''").
		Count(&pinned).Error
	if err != nil || pinned == 0 {
		return
	}

	log.Print(common.LogText(
		"WARNING: %d access token(s) restrict the client address, but TRUSTED_PROXIES is unset so every private range is trusted as a proxy. "+
			"Bind the container port to the reverse proxy (for example 127.0.0.1:3000:3000) or set TRUSTED_PROXIES explicitly; "+
			"otherwise a host on the same network can forge X-Forwarded-For and pass that check.",
		pinned,
	))
}

// APIDUO:token-ip-allowlist:END
