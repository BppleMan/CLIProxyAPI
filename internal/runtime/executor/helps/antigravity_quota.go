package helps

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ParseAntigravityQuotaSummary follows the QuotaSummaryBucket descriptor in agy.
// It does not infer missing values, subscription capacity, or a five-hour reset.
func ParseAntigravityQuotaSummary(data []byte, observedAt time.Time) (coreauth.RPQuotaSnapshot, error) {
	var root struct {
		Buckets []json.RawMessage `json:"buckets"`
		Groups  []struct {
			Buckets []json.RawMessage `json:"buckets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return coreauth.RPQuotaSnapshot{}, errors.New("invalid quota summary")
	}
	rawBuckets := root.Buckets
	// The descriptor marks root buckets deprecated; groups is authoritative.
	if len(root.Groups) > 0 {
		rawBuckets = nil
		for _, group := range root.Groups {
			rawBuckets = append(rawBuckets, group.Buckets...)
		}
	}
	out := coreauth.RPQuotaSnapshot{ObservedAt: observedAt}
	seen := make(map[string]bool)
	for _, raw := range rawBuckets {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return out, errors.New("invalid quota bucket")
		}
		field := func(camel, snake string) json.RawMessage {
			if v, ok := fields[camel]; ok {
				return v
			}
			return fields[snake]
		}
		var bucket coreauth.RPQuotaBucket
		_ = json.Unmarshal(field("bucketId", "bucket_id"), &bucket.ID)
		_ = json.Unmarshal(fields["window"], &bucket.Window)
		if bucket.ID == "" || seen[bucket.ID] {
			return out, errors.New("missing or duplicate quota bucket ID")
		}
		seen[bucket.ID] = true
		if raw := field("remainingFraction", "remaining_fraction"); len(raw) > 0 {
			var f float64
			if string(raw) == "null" || json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1 {
				return out, errors.New("invalid quota fraction")
			}
			bucket.RemainingFraction = &f
		}
		if raw := field("remainingAmount", "remaining_amount"); len(raw) > 0 {
			var number json.Number
			if string(raw) == "null" || json.Unmarshal(raw, &number) != nil {
				return out, errors.New("invalid quota amount")
			}
			n, err := strconv.ParseInt(number.String(), 10, 64)
			if err != nil || n < 0 {
				return out, errors.New("invalid quota amount")
			}
			bucket.RemainingAmount = &n
		}
		if bucket.RemainingFraction == nil && bucket.RemainingAmount == nil {
			return out, errors.New("unknown quota remaining")
		}
		if bucket.RemainingFraction != nil && bucket.RemainingAmount != nil {
			return out, errors.New("conflicting quota remaining oneof")
		}
		if raw := fields["disabled"]; len(raw) > 0 && json.Unmarshal(raw, &bucket.Disabled) != nil {
			return out, errors.New("invalid quota disabled flag")
		}
		if raw := field("resetTime", "reset_time"); len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &bucket.ResetAt); err != nil {
				return out, errors.New("invalid quota reset time")
			}
		}
		out.Buckets = append(out.Buckets, bucket)
	}
	if len(out.Buckets) == 0 {
		return out, errors.New("empty quota summary")
	}
	return out, nil
}
