package endpoint

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchResolvesPathImpliedNames(t *testing.T) {
	spec, ok := Match("POST", "/v1/tools/search")
	require.True(t, ok)
	assert.Equal(t, ToolsSearchModel, spec.Model)
	assert.Equal(t, []types.EndpointType{types.EndpointTypeDeepSeekToolsSearch}, spec.Endpoints)

	// A body-declared model must not change the routing name; the path wins.
	spec, ok = Match("POST", "/v1/tools/fetch")
	require.True(t, ok)
	assert.Equal(t, ToolsFetchModel, spec.Model)
}

func TestMatchIgnoresMethodAndUnknownPaths(t *testing.T) {
	_, ok := Match("GET", "/v1/tools/search")
	assert.False(t, ok, "the endpoint is POST only")
	_, ok = Match("POST", "/v1/tools/search/extra")
	assert.False(t, ok, "matching is exact, not a prefix")
	_, ok = Match("POST", "/v1/chat/completions")
	assert.False(t, ok, "a model-declared endpoint has no routing name to supply")
}

func TestFamilyEntriesAreNotMatchedByPath(t *testing.T) {
	// An upload's name needs the purpose, which is in the multipart body and
	// not readable when the distributor runs. The handler composes it.
	_, ok := Match("POST", "/v1/files")
	assert.False(t, ok)
}

func TestLookupResolvesFamilyMembers(t *testing.T) {
	spec, ok := Lookup(FileUploadModel("file-extract"))
	require.True(t, ok)
	assert.True(t, spec.Family)
	assert.Equal(t, []types.EndpointType{types.EndpointTypeFileUpload}, spec.Endpoints)

	purpose, ok := FileUploadPurpose(FileUploadModel("file-extract"))
	require.True(t, ok)
	assert.Equal(t, "file-extract", purpose)

	_, ok = FileUploadPurpose(FileUploadPrefix)
	assert.False(t, ok, "an empty purpose is not a name any channel can declare")
}

func TestEndpointTypesDistinguishesUnknownFromEmpty(t *testing.T) {
	got, ok := EndpointTypes(ToolsSearchModel)
	require.True(t, ok)
	assert.Equal(t, []types.EndpointType{types.EndpointTypeDeepSeekToolsSearch}, got)

	_, ok = EndpointTypes("deepseek-flash")
	assert.False(t, ok, "a real model must fall through to the channel-type rule")
}

func TestIsSyntheticCoversTableAndReservedNamespace(t *testing.T) {
	assert.True(t, IsSynthetic(ToolsSearchModel))
	assert.True(t, IsSynthetic(FileUploadModel("batch")))
	// A stale row left behind by a removed spec still must not be advertised.
	assert.True(t, IsSynthetic(Prefix+"removed/endpoint"))
	assert.False(t, IsSynthetic("gpt-4o"))
}

func TestDefaultInfoUsesTheTable(t *testing.T) {
	info, ok := DefaultInfo(types.EndpointTypeFileUpload)
	require.True(t, ok)
	assert.Equal(t, "POST", info.Method)
	assert.Equal(t, "/v1/files", info.Path)

	_, ok = DefaultInfo(types.EndpointTypeOpenAI)
	assert.False(t, ok, "endpoints the table does not own keep their own map")
}

func TestEveryPricedSpecCarriesARequestTier(t *testing.T) {
	for _, spec := range All() {
		if spec.Price == "" {
			continue
		}
		// The calls carry no tokens, so the expression must price the request
		// itself; a token term would silently bill zero.
		assert.Contains(t, spec.Price, `tier("request"`, spec.ID)
		assert.Contains(t, spec.Price, "fixed(", spec.ID)
	}
}
