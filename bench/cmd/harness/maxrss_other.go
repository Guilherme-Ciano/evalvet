//go:build !linux && !darwin

package main

import "os"

func maxRSSKB(*os.ProcessState) int64 {
	return 0
}
