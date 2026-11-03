//go:build !linux && !darwin

package node

func diskGiB() int64 { return 0 }
