// Package rpmode gates the private, opt-in RP experiment.
package rpmode

import "os"

func Enabled() bool { return os.Getenv("CPA_PRIVATE_RP") == "1" }
