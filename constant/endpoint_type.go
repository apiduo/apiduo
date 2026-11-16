package constant

import (
	"github.com/QuantumNous/new-api/pkg/endpoint"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// EndpointType moved to types with the conversion kit; aliases keep host
// code compiling unchanged.
type EndpointType = types.EndpointType

const (
	EndpointTypeOpenAI                = types.EndpointTypeOpenAI
	EndpointTypeOpenAIResponse        = types.EndpointTypeOpenAIResponse
	EndpointTypeOpenAIResponseCompact = types.EndpointTypeOpenAIResponseCompact
	EndpointTypeOpenAIAlphaSearch     = types.EndpointTypeOpenAIAlphaSearch
	EndpointTypeDeepSeekToolsSearch   = types.EndpointTypeDeepSeekToolsSearch
	EndpointTypeDeepSeekToolsFetch    = types.EndpointTypeDeepSeekToolsFetch
	EndpointTypeAnthropic             = types.EndpointTypeAnthropic
	EndpointTypeGemini                = types.EndpointTypeGemini
	EndpointTypeJinaRerank            = types.EndpointTypeJinaRerank
	EndpointTypeImageGeneration       = types.EndpointTypeImageGeneration
	EndpointTypeEmbeddings            = types.EndpointTypeEmbeddings
	EndpointTypeOpenAIVideo           = types.EndpointTypeOpenAIVideo
	EndpointTypeFileUpload            = types.EndpointTypeFileUpload
)

// Model names the distributor assigns to /v1/tools/search and /v1/tools/fetch,
// whose bodies carry no model. Channels serve the endpoints by listing them.
//
// APIDUO:endpoint-registry:BEGIN The names are owned by pkg/endpoint, which
// also carries the endpoint type and the price of each one. These aliases exist
// because the vendor preset lists them as models.
const (
	DeepSeekSearchModel = endpoint.ToolsSearchModel
	DeepSeekFetchModel  = endpoint.ToolsFetchModel
)

// APIDUO:endpoint-registry:END
