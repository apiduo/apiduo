package constant

import "testing"

// Apiduo-only channel types must stay outside the range upstream keeps growing
// into. Upstream hands out the next channel type by moving ChannelTypeDummy, so
// a type inserted just below it collides on the next sync and the conflict
// resolution can silently renumber a live channel. 8000+ is the reserved range.
func TestApiduoChannelTypesStayOutsideUpstreamRange(t *testing.T) {
	if ChannelTypeAdvancedAsync < 8000 {
		t.Fatalf("apiduo-only channel type %d must stay at 8000 or above", ChannelTypeAdvancedAsync)
	}
	if ChannelTypeAdvancedAsync <= ChannelTypeDummy {
		t.Fatalf("apiduo-only channel type %d must stay above ChannelTypeDummy (%d)",
			ChannelTypeAdvancedAsync, ChannelTypeDummy)
	}
}
