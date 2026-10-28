//go:build !linux && !darwin

package management

func privateDiskUsage(_ string) (total, available uint64, ok bool) {
	return 0, 0, false
}
