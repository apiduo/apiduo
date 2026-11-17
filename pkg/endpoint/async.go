package endpoint

// This file registers endpoints whose response shape is asynchronous by
// contract. It is deliberately separate from specs.
//
// Spec above answers a different question: it gives a routing name to an
// endpoint whose body carries no model, because abilities and Distribute()
// need a key that only a model string can provide. An asynchronous image
// endpoint does carry one — the client's model is what selects the channel —
// so it must not enter specs. Spec.Model doubles as a billing name and as the
// marker IsSynthetic uses to hide a name from the model list; an entry with an
// empty Model would make Lookup("") answer true and hide nothing.
//
// What this table fixes is the shape, not the route. A client that calls an
// asynchronous path receives the asynchronous shape whatever the winning
// channel turns out to be: a channel that answers with an upstream task id and
// a channel that completes inline both produce {id, status} rather than two
// different bodies. The protocol vocabulary is the one advanced-async channels
// already speak, so the existing submit / poll / render pipeline serves these
// paths without a second implementation.

// AsyncProtocolImages is the OpenAI Images asynchronous wire shape: the submit
// half answers with a task id, and the poll half answers with a standard Images
// response once the task is terminal.
//
// constant.ApiduoAsyncProtocolImages aliases this value: constant already
// imports this package, so the name has exactly one definition here and the
// advanced-async channel configuration keeps reading the same string.
const AsyncProtocolImages = "images"

// AsyncImagePath is the built-in asynchronous entry of the Images API.
//
// The /async prefix, placed before /v1, is what distinguishes it from the
// synchronous /v1/images/generations without a query parameter: the whole path
// is the contract, so a client cannot accidentally receive the other shape by
// omitting an argument. The prefix is stripped before the request re-enters the
// ordinary relay path, which means no upstream ever sees it.
//
// The older /v1/images/generations/async entry still works: it is a path a
// channel declares in advanced_async.incoming_paths, and removing it would take
// already-configured channels offline. The two differ in who owns the path —
// the channel or the gateway — not in what the client receives.
const AsyncImagePath = "/async/v1/images/generations"

// AsyncImageSubmitPath is where the submit half of the Images async protocol is
// served. It is an Images path on purpose: an image task that re-entered the
// video relay path would be logged, presented and filtered as video work, and
// only the upstream shape would remain image-specific.
//
// It is a static route, so a client may call it directly; AsyncImagePath is the
// prefixed alias the gateway also owns and rewrites onto this path.
const AsyncImageSubmitPath = "/v1/images/generations/async"

// AsyncImageSyncPath is the synchronous Images endpoint. The async entry never
// forwards to it on the client's behalf, but the gateway itself uses it as the
// request's identity when it has to ask a channel's adaptor where that channel
// puts its images: the adaptor keys on the path, not on a hardcoded constant.
const AsyncImageSyncPath = "/v1/images/generations"

// AsyncEndpoint is one path whose shape the gateway owns.
type AsyncEndpoint struct {
	ID string
	// Path is the client-facing path. It carries no task id; the poll half
	// appends one and the caller splits it off before matching.
	Path string
	// Protocol is the wire shape. It selects the existing advanced-async
	// handler, not a new one.
	Protocol string
	// Submit is the path the submit half is actually served at. When it equals
	// Path the request is already on its landing route and must not be
	// rewritten again.
	Submit string
}

var asyncEndpoints = []AsyncEndpoint{
	{
		ID:       "images.async",
		Path:     AsyncImagePath,
		Protocol: AsyncProtocolImages,
		Submit:   AsyncImageSubmitPath,
	},
	{
		// The declared entry, now also a static route. Registering it here is
		// what makes the endpoint filter keep synchronous channels out of it;
		// before, the relay-path exemption let any channel listing the model in.
		ID:       "images.async.declared",
		Path:     AsyncImageSubmitPath,
		Protocol: AsyncProtocolImages,
		Submit:   AsyncImageSubmitPath,
	},
}

// LookupAsync reports the asynchronous endpoint served at an exact path.
//
// Method is not part of the key on purpose: a submit and its polls share one
// path, and the caller has already branched on the method to decide whether a
// trailing id is expected. Matching by path alone keeps a GET and a POST of the
// same endpoint from drifting into two different answers.
func LookupAsync(path string) (AsyncEndpoint, bool) {
	for _, spec := range asyncEndpoints {
		if spec.Path == path {
			return spec, true
		}
	}
	return AsyncEndpoint{}, false
}
