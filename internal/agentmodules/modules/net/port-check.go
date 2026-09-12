// TCP port reachability check — no nc/telnet dependency. Args: <host:port>.
// Prints open/closed; exit 0 when open, 1 when closed/refused/timeout.
package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("port-check: usage: port-check <host:port>")
		os.Exit(1)
	}
	addr := os.Args[1]
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		fmt.Printf("state=closed target=%s error=%v\n", addr, err)
		os.Exit(1)
	}
	_ = conn.Close()
	fmt.Printf("state=open target=%s\n", addr)
}
