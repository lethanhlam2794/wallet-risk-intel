package scoring

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPolicyV1_1_Version(t *testing.T) {
	assert.Equal(t, "v1.1.0", PolicyV1_1)
	assert.Equal(t, "v1.0.0", PolicyV1, "v1 constant is frozen")
}

func TestKnownPolicy_Registry(t *testing.T) {
	for _, v := range []string{PolicyV1, PolicyV1_1} {
		assert.True(t, KnownPolicy(v), v+" must be registered")
	}
	for _, v := range []string{"v1.2.0", "", "v2.0.0", "V1.1.0", "1.1.0"} {
		assert.False(t, KnownPolicy(v), v+" must not be registered")
	}
}

func TestPolicyV1_1_Constants(t *testing.T) {
	assert.Equal(t, 70, exposureCapV1_1)
	assert.Equal(t, 30, exposureSourceBaseV1_1)
	assert.Equal(t, 7, exposureSourceFreqV1_1)
	assert.Equal(t, 21, exposureReportedSourceV1_1)
	assert.Equal(t, 100, meaningfulFloorUSDV1_1)
	assert.Equal(t, 5000, ExposureScanLimitV1_1)
	assert.Equal(t, 256, ExposureMaxSourcesV1_1)
	assert.Equal(t, 512, ExposureMaxDepth2SendersV1_1)
	assert.Equal(t, 10, ExposureEvidenceTxsV1_1)
}

// The direct-taint set is the proposal §3.1 list — the same 11 codes as
// baseWeightsV1 — asserted verbatim so a taint/scored-set drift fails.
func TestPolicyV1_1_DirectTaintSet(t *testing.T) {
	want := map[string]bool{
		"SANCTIONED": true, "HACK": true, "RANSOMWARE": true,
		"PHISHING": true, "TERRORISM": true, "SCAM": true, "DRAINER": true,
		"MONEY_LAUNDERING": true, "DARKNET": true, "EXTREMISM": true,
		"FROZEN": true,
	}
	assert.Equal(t, want, directTaintCategoriesV1_1)
	for cat := range want {
		_, scored := baseWeightsV1[cat]
		assert.True(t, scored, cat+" taints but carries no v1 base weight")
	}
}

// Service classifications gate suppression — the §3.4 list verbatim.
func TestPolicyV1_1_ServiceClassifications(t *testing.T) {
	want := map[string]bool{"EXCHANGE": true, "GAMBLING": true, "MIXER": true}
	assert.Equal(t, want, serviceClassificationsV1_1)
	for cat := range want {
		assert.False(t, directTaintCategoriesV1_1[cat],
			cat+" is a classification, never a taint source")
	}
}
