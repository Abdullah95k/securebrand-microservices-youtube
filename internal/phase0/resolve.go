package phase0

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// ResolveList looks up each channel input (handle, URL or ID) and prints
// whether it was found and, when not, what YouTube answered. It reports
// whether every input was found. Inputs and channel IDs are public; this
// prints nothing else.
func ResolveList(ctx context.Context, w io.Writer, col *Collector, inputs []string) bool {
	found := 0
	for _, in := range inputs {
		if ctx.Err() != nil {
			break
		}
		in = strings.TrimSpace(in)
		if in == "" {
			continue
		}
		var cost Cost
		id, err := col.Resolve(ctx, in, &cost)
		if err != nil {
			fmt.Fprintf(w, "NOT FOUND  %s\n           %v\n", in, err)
			continue
		}
		found++
		fmt.Fprintf(w, "found      %s  ->  %s\n", in, id)
	}
	fmt.Fprintf(w, "\nResolved %d of %d channel(s).\n", found, len(nonEmpty(inputs)))
	return found == len(nonEmpty(inputs))
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
