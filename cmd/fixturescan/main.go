// Command fixturescan fails when the repository contains data that must never
// be committed to this public repo: real-looking YouTube channel IDs, Google
// API keys, passwords in URLs, email addresses or phone numbers.
//
// Usage:
//
//	go run ./cmd/fixturescan [directory]
package main

import (
	"fmt"
	"os"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fixturescan"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := fixturescan.ScanTree(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixturescan:", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Fprintf(os.Stderr, "fixturescan: %d finding(s). Use generated fixtures and %s… fake channel IDs; never commit real data or secrets.\n",
			len(findings), fixturescan.FakeChannelPrefix)
		os.Exit(1)
	}
	fmt.Println("fixturescan: clean")
}
