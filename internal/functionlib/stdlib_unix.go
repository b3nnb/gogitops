// Stdlib, unix-only part. statfs has no portable equivalent on windows —
// the function simply doesn't exist there (recipe fails cleanly with
// "function not available", which versions.yaml gating already covers).
//
//go:build !windows

package functionlib

import (
	"fmt"
	"syscall"
)

func init() {
	// storage.disk-free — port of the modules/system/disk-free.go module
	// to a typed native function. Same math, no shell, outputs feed
	// later steps as {{func.total_gb}} etc.
	Register(Function{
		Name:        "storage.disk-free",
		Description: "Disk space with the math shown: total/used/free GB + used pct for a mount point.",
		Params: []Param{
			{Name: "path", Type: "string", Description: "Mount point to stat (default /)"},
		},
		Run: diskFree,
	})
}

func diskFree(ctx Context, args map[string]any) (map[string]any, error) {
	path := "/"
	if p, ok := args["path"].(string); ok && p != "" {
		path = p
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, fmt.Errorf("cannot stat %s: %w", path, err)
	}
	// math: GB = blocks × blocksize / 2^30 ; pct = (total-free)/total
	bs := float64(stat.Bsize)
	total := float64(stat.Blocks) * bs / (1 << 30)
	avail := float64(stat.Bavail) * bs / (1 << 30)
	used := float64(stat.Blocks-stat.Bfree) * bs / (1 << 30)
	pct := 0.0
	if total > 0 {
		pct = used / total * 100
	}
	return map[string]any{
		"point":    path,
		"total_gb": fmt.Sprintf("%.1f", total),
		"used_gb":  fmt.Sprintf("%.1f", used),
		"free_gb":  fmt.Sprintf("%.1f", avail),
		"used_pct": fmt.Sprintf("%.0f", pct),
		"math":     "(blocks×bs)/2^30",
	}, nil
}
