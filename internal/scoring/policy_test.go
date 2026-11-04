package scoring

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPolicyVersion(t *testing.T) {
	assert.Equal(t, "v1.0.0", PolicyV1)
}

func TestPolicyTables(t *testing.T) {
	want := map[string]int{
		"RANSOMWARE": 95, "HACK": 90, "PHISHING": 90, "TERRORISM": 90,
		"SCAM": 85, "DRAINER": 85, "SANCTIONED": 85, "MONEY_LAUNDERING": 80,
		"DARKNET": 75, "EXTREMISM": 70, "FROZEN": 60,
	}
	assert.Equal(t, want, baseWeightsV1)

	assert.Equal(t, 10, verificationFactorsV1["verified"])
	assert.Equal(t, 7, verificationFactorsV1["reported"])
	assert.Equal(t, 5, verificationFactorsV1["unverified"])
	assert.Equal(t, 0, verificationFactorsV1["disputed"])

	assert.Equal(t, 10, confidenceFactorsV1["high"])
	assert.Equal(t, 9, confidenceFactorsV1["medium"])
	assert.Equal(t, 8, confidenceFactorsV1["low"])
	assert.Equal(t, 7, confidenceFactorsV1["unknown"])
}

func TestClassificationCategoriesAbsent(t *testing.T) {
	// GAMBLING/MIXER/EXCHANGE are deliberately absent — they route to
	// the zero-weight classification path, never scored.
	for _, cat := range []string{"GAMBLING", "MIXER", "EXCHANGE", "OTHER"} {
		_, ok := baseWeightsV1[cat]
		assert.False(t, ok, cat+" must not carry a base weight")
	}
}

func TestProviderScoreBand(t *testing.T) {
	assert.Equal(t, 20, providerScoreBandV1(80))
	assert.Equal(t, 20, providerScoreBandV1(100))
	assert.Equal(t, 10, providerScoreBandV1(50))
	assert.Equal(t, 10, providerScoreBandV1(79))
	assert.Equal(t, 0, providerScoreBandV1(49))
	assert.Equal(t, 0, providerScoreBandV1(0))
}
