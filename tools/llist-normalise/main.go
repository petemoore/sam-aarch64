// llist-normalise applies a fixed set of normalisation rules to
// spike-vs-llist BASIC listing capture pairs so they can be
// byte-compared. Reads a corpus directory (default
// ~/detok-captures/), produces a parallel directory tree under the
// --out path with each *.spike.txt and *.llist.txt transformed by
// the appropriate per-side rules. Writes a README into the output
// dir documenting every rule.
//
// See rules.go for the rule implementations and rules_test.go for
// the per-rule unit tests.
package main

import (
	"fmt"
	"log"
	"os"
)

func main() {
	// CLI orchestration lives in a later task. For now the binary
	// just confirms it's built.
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("llist-normalise v0 (scaffold)")
		return
	}
	log.Fatal("llist-normalise: CLI not yet implemented; see plan task list")
}
