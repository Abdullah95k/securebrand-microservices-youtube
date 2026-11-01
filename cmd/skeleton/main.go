// Command skeleton prints the structure of a JSON file with every value
// replaced by a type placeholder, so a response's shape can be shared
// without its data. Review the output before sharing or committing it.
//
// Usage:
//
//	go run ./cmd/skeleton response.json > response.skeleton.json
//	cat response.json | go run ./cmd/skeleton
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/skeleton"
)

func main() {
	var in io.Reader = os.Stdin
	if len(os.Args) > 1 {
		f, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "skeleton:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, 64<<20))
	if err != nil {
		fmt.Fprintln(os.Stderr, "skeleton:", err)
		os.Exit(1)
	}
	out, err := skeleton.Bytes(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skeleton: input is not valid JSON:", err)
		os.Exit(1)
	}
	os.Stdout.Write(append(out, '\n'))
}
