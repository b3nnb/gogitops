// USER SPACE example (benn.* namespace — anything but storage/net/system).
// User modules call stdlib functions via `gogitops func-run <name>` or a
// func: step feeding them; they can never define files in reserved sets.
// Drop your own under modules/<your-set>/ and call from any recipe:
//
//	script: benn/hello-fleet.go
package main

import (
	"fmt"
	"os"
)

func main() {
	who := "fleet"
	if len(os.Args) > 1 && os.Args[1] != "" {
		who = os.Args[1]
	}
	fmt.Printf("hello=%s math=printf, no shell quoting needed message=hi %s\n", who, who)
}
