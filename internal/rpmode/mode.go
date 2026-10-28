// Package rpmode gates the private, opt-in RP experiment.
package rpmode

import "os"

func Enabled() bool { return os.Getenv("CPA_PRIVATE_RP") == "1" }

// RequestLoggingEnabled requires an explicit opt-in for sensitive RP transcripts.
func RequestLoggingEnabled() bool {
	return Enabled() && os.Getenv("CPA_PRIVATE_RP_REQUEST_LOG") == "1"
}
