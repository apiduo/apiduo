package model

// APIDUO:file-relay:BEGIN
//
// A channel serves a file purpose by listing the purpose's routing name in its
// model list, the same way it serves a model. The only file-specific channel
// attribute left is the per-file ceiling, because a ceiling is a number rather
// than a capability and has no place in a model name.
func FileUploadMaxBytes(channel *Channel) int64 {
	fileRelay := channel.GetOtherSettings().FileRelay
	if fileRelay == nil {
		return 0
	}
	return fileRelay.MaxUploadBytes
}

// APIDUO:file-relay:END
