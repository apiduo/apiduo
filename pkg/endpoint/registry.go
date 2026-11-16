// Package endpoint is the single table of downstream endpoints whose request
// does not name a model.
//
// The gateway selects a channel from a model string: abilities is keyed by
// (group, model, channel_id) and Distribute() rejects a request without one.
// An endpoint such as POST /v1/files or POST /v1/tools/search carries no model
// in its body, so the endpoint itself has to supply a routing name. Before this
// table existed that name was hardcoded in four places — the distributor's
// path chain, GetEndpointTypesByChannelType's name switch, the default endpoint
// map, and the built-in billing map — and every new endpoint had to find all
// four.
//
// One Spec now feeds all four. Adding an endpoint is adding one entry here;
// no other file learns its name.
package endpoint

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// Prefix marks a routing name that no upstream model name can collide with.
// Every entry added after this table existed must use it: the routing name is
// also the billing name, so a vendor model that happens to share it would be
// silently repriced and rerouted.
//
// Two entries predate the rule. They keep their original names because channels
// already list them and a rename would take the endpoint offline on upgrade;
// they are marked legacy below.
const Prefix = "@"

// FileUploadPrefix is the routing-name family of one file upload. The purpose
// is part of the name because purpose is exactly what makes a channel eligible:
// a channel serves file-extract by listing "@files/upload/file-extract", the
// same way it serves a model by listing the model.
const FileUploadPrefix = Prefix + "files/upload/"

// Routing names of the model-less tool endpoints.
const (
	// Legacy names kept from the vendor preset that introduced them.
	ToolsSearchModel = "deepseek-search"
	ToolsFetchModel  = "deepseek-fetch"
)

// Spec describes one endpoint.
type Spec struct {
	ID string
	// Method and Path identify the request when the routing name is fixed by
	// the path. Family entries leave Path unused for matching: their name needs
	// a value from the request body.
	Method string
	Path   string
	// Model is the routing and billing name. For a Family it is the prefix.
	Model  string
	Family bool
	// Endpoints is the capability written into abilities for this name. It is
	// what the endpoint filter uses to keep a channel that cannot serve this
	// surface out of the candidate set.
	Endpoints []types.EndpointType
	// Price is a built-in billing expression in USD, empty when the handler
	// charges on its own (per byte, for uploads).
	Price string
}

var specs = []Spec{
	{
		ID:        "tools.search",
		Method:    "POST",
		Path:      "/v1/tools/search",
		Model:     ToolsSearchModel,
		Endpoints: []types.EndpointType{types.EndpointTypeDeepSeekToolsSearch},
		Price:     `tier("request", fixed(0.00075))`,
	},
	{
		ID:        "tools.fetch",
		Method:    "POST",
		Path:      "/v1/tools/fetch",
		Model:     ToolsFetchModel,
		Endpoints: []types.EndpointType{types.EndpointTypeDeepSeekToolsFetch},
		Price:     `tier("request", fixed(0.00075))`,
	},
	{
		ID:     "files.upload",
		Method: "POST",
		Path:   "/v1/files",
		Model:  FileUploadPrefix,
		Family: true,
		// An upload moves bytes, not tokens: the handler reserves quota per
		// megabyte, so there is no per-request expression to seed.
		Endpoints: []types.EndpointType{types.EndpointTypeFileUpload},
	},
}

// All returns every spec. Callers must not mutate the result.
func All() []Spec { return specs }

// Match returns the spec whose routing name is implied by this request path.
// Family entries never match: their name needs a value the body carries, so
// their handler composes it and calls Lookup instead.
func Match(method, path string) (Spec, bool) {
	for _, spec := range specs {
		if spec.Family || spec.Method != method || spec.Path != path {
			continue
		}
		return spec, true
	}
	return Spec{}, false
}

// Lookup resolves a routing name, including a member of a family.
func Lookup(model string) (Spec, bool) {
	for _, spec := range specs {
		if spec.Family {
			if strings.HasPrefix(model, spec.Model) && len(model) > len(spec.Model) {
				return spec, true
			}
			continue
		}
		if spec.Model == model {
			return spec, true
		}
	}
	return Spec{}, false
}

// EndpointTypes reports the capabilities a routing name implies. The boolean
// distinguishes "no such endpoint" from "an endpoint with no capability", which
// callers must answer differently.
func EndpointTypes(model string) ([]types.EndpointType, bool) {
	spec, ok := Lookup(model)
	if !ok {
		return nil, false
	}
	return spec.Endpoints, true
}

// IsSynthetic reports a name that no client typed and no upstream serves. Such
// a name must stay out of the model list a caller browses, because it is not
// something a caller can send to /v1/chat/completions.
func IsSynthetic(model string) bool {
	if _, ok := Lookup(model); ok {
		return true
	}
	// A name under the reserved prefix is ours even when its spec was removed:
	// an operator may still hold a stale row, and presenting it as a model
	// would invite requests nothing can serve.
	return strings.HasPrefix(model, Prefix)
}

// DefaultInfo reports the request shape of an endpoint capability, used where
// the console describes what a channel can serve.
func DefaultInfo(endpointType types.EndpointType) (EndpointInfo, bool) {
	for _, spec := range specs {
		for _, candidate := range spec.Endpoints {
			if candidate == endpointType {
				return EndpointInfo{Method: spec.Method, Path: spec.Path}, true
			}
		}
	}
	return EndpointInfo{}, false
}

// EndpointInfo is the method and path of one endpoint.
type EndpointInfo struct {
	Path   string
	Method string
}

// FileUploadModel composes the routing name of one upload. An empty purpose is
// left to the caller to reject with a client error rather than silently
// producing a name no channel can declare.
func FileUploadModel(purpose string) string { return FileUploadPrefix + purpose }

// FileUploadPurpose recovers the purpose from a routing name and reports
// whether the name belongs to that family.
func FileUploadPurpose(model string) (string, bool) {
	if !strings.HasPrefix(model, FileUploadPrefix) {
		return "", false
	}
	purpose := strings.TrimPrefix(model, FileUploadPrefix)
	if purpose == "" {
		return "", false
	}
	return purpose, true
}
