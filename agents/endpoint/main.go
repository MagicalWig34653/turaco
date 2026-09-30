package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	printCapabilities := flag.Bool("capabilities", false, "print supported capability identifiers")
	flag.Parse()
	if *printCapabilities {
		_ = json.NewEncoder(os.Stdout).Encode([]string{"inventory.collect"})
		return
	}
	fmt.Printf("endpoint-agent %s: enrollment/management transport not implemented yet; see docs/security/agent-boundaries.md\n", version)
}
