package helps

import (
	"testing"
	"time"
)

func TestPrivateRPQuotaSummaryContract(t *testing.T) {
	for _, test := range []struct {
		name, data string
		valid      bool
	}{
		{"grouped", `{"groups":[{"buckets":[{"bucketId":"gemini-five-hour","window":"5h","remainingFraction":0,"resetTime":"2026-10-25T10:00:00Z"},{"bucketId":"gemini-weekly","remainingFraction":0.8}]}]}`, true},
		{"legacy-proto-int64", `{"buckets":[{"bucketId":"requests","remainingAmount":"123"}]}`, true},
		{"snake-case", `{"groups":[{"buckets":[{"bucket_id":"gemini","remaining_fraction":0.2,"reset_time":"2026-10-25T10:00:00Z"}]}]}`, true},
		{"unknown", `{"buckets":[{"bucketId":"unknown"}]}`, false},
		{"conflicting-oneof", `{"buckets":[{"bucketId":"bad","remainingFraction":0.5,"remainingAmount":"5"}]}`, false},
		{"null", `{"buckets":[{"bucketId":"unknown","remainingFraction":null}]}`, false},
		{"fraction-over-one", `{"buckets":[{"bucketId":"bad","remainingFraction":1.2}]}`, false},
		{"negative", `{"buckets":[{"bucketId":"bad","remainingAmount":"-1"}]}`, false},
		{"duplicate", `{"buckets":[{"bucketId":"same","remainingFraction":0.2},{"bucketId":"same","remainingFraction":1}]}`, false},
		{"missing-id", `{"buckets":[{"remainingFraction":1}]}`, false},
		{"invalid-reset", `{"buckets":[{"bucketId":"bad","remainingFraction":0,"resetTime":"tomorrow"}]}`, false},
		{"empty", `{}`, false},
		{"html", `<html>rate limited</html>`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := ParseAntigravityQuotaSummary([]byte(test.data), time.Now())
			if (err == nil) != test.valid {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}
