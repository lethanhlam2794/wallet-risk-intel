package sanctions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reason model must never synthesize text: OFFICIAL_EXPLICIT only with
// real source text, PROGRAM_CONTEXT_ONLY with programs, else NOT_AVAILABLE
// with a null summary.
func TestReasonFor_ThreeStatuses(t *testing.T) {
	official := "OFAC press release reason"
	r := reasonFor(&official, 2)
	assert.Equal(t, ReasonOfficialExplicit, r.Status)
	require.NotNil(t, r.Summary)
	assert.Equal(t, official, *r.Summary)

	r = reasonFor(nil, 2)
	assert.Equal(t, ReasonProgramContextOnly, r.Status)
	assert.Nil(t, r.Summary)

	r = reasonFor(nil, 0)
	assert.Equal(t, ReasonNotAvailable, r.Status)
	assert.Nil(t, r.Summary)

	// Empty official text behaves as no official text.
	empty := ""
	r = reasonFor(&empty, 1)
	assert.Equal(t, ReasonProgramContextOnly, r.Status)
}
