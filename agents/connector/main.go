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
		_ = json.NewEncoder(os.Stdout).Encode([]string{
			"ldap.users.read",
			"ldap.groups.read",
			"ldap.authenticate",
		})
		return
	}
	fmt.Printf("connector-agent %s: transport/enrollment not implemented yet; see docs/security/agent-boundaries.md\n", version)
}
