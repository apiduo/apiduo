package common

// ArtifactsHandledByAdaptor reports whether the adaptor parses and re-encodes
// this response instead of passing the upstream body through.
//
// An image-protocol channel is exactly that case: the upstream Gemini/ARK body
// is decoded into a canonical form and re-encoded into the entry's shape. The
// generic artifact rewriter must stay away from such a body — it runs before
// DoResponse, so it would replace the base64 payloads the adaptor still needs
// to read with storage links, and every downstream decoder then fails on a
// value that is no longer base64.
//
// Channels that pass the body through are unaffected: they are the rewriter's
// intended audience.
//
// 放在新文件而不是 relay_info.go，是为了不把合并面压到上游文件上。
func (info *RelayInfo) ArtifactsHandledByAdaptor() bool {
	if info == nil || info.ChannelMeta == nil {
		return false
	}
	protocol := info.ChannelOtherSettings.ImageProtocol
	return protocol != nil && protocol.Enabled
}
