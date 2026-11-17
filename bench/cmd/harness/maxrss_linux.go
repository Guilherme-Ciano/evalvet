package main

import (
	"os"
	"syscall"
)

func maxRSSKB(ps *os.ProcessState) int64 {
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss
	}
	return 0
}
