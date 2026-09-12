// Disk free space with the math shown: total, used, free in GB + used pct.
// Args: <path> (default: /). Attr-friendly output for set_attr steps.
package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func main() {
	path := "/"
	if len(os.Args) > 1 && os.Args[1] != "" {
		path = os.Args[1]
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		fmt.Printf("disk-free: cannot stat %s: %v\n", path, err)
		os.Exit(1)
	}
	// math: GB = blocks × blocksize / 2^30 ; pct = (total-free)/total
	bs := float64(stat.Bsize)
	total := float64(stat.Blocks) * bs / (1 << 30)
	free := float64(stat.Bavail) * bs / (1 << 30)
	used := float64(stat.Blocks-stat.Bfree) * bs / (1 << 30)
	pct := 0.0
	if total > 0 {
		pct = used / total * 100
	}
	fmt.Printf("point=%s total_gb=%s used_gb=%s free_gb=%s used_pct=%s math=(blocks×bs)/2^30\n",
		path,
		strconv.FormatFloat(total, 'f', 1, 64),
		strconv.FormatFloat(used, 'f', 1, 64),
		strconv.FormatFloat(free, 'f', 1, 64),
		strconv.FormatFloat(pct, 'f', 0, 64))
}
