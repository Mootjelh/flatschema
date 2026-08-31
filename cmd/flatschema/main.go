// Command flatschema infers a FlatBuffers schema from sample buffers.
//
//	flatschema payload.bin > schema.fbs
//	flatschema *.bin > schema.fbs
//	flatschema -depth 20 -elems 5 a.bin b.bin
//
// More samples give a better schema. A slot is only visible when a buffer
// populates it, so a field no sample uses cannot be seen at all.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Mootjelh/flatschema"
)

func main() {
	depth := flag.Int("depth", flatschema.MaxDepth, "how far to follow nested tables")
	elems := flag.Int("elems", 0, "how many elements of a table vector to examine, 0 for all")
	quiet := flag.Bool("q", false, "no summary on stderr")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: flatschema [flags] buffer.bin [buffer.bin ...]

Reads FlatBuffers buffers with no schema and writes the .fbs they imply to
stdout. Size-prefixed buffers are detected.

Give it as many samples as you have. A slot only shows up when some buffer
populates it, so one sample yields the fields that one buffer used and nothing
else.

What comes out is a skeleton to edit, not the original file. Field names are
not in the data, and neither are scalar widths. Every field carries a comment
saying what was actually seen in that slot.

flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	buffers := make([][]byte, 0, flag.NArg())
	for _, path := range flag.Args() {
		buf, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		buffers = append(buffers, buf)
	}

	schema, err := flatschema.InferWith(flatschema.Options{MaxDepth: *depth, Elems: *elems}, buffers...)
	if err != nil {
		fail(err)
	}

	fmt.Print(schema.FBS())

	if !*quiet {
		// On stderr so that redirecting stdout to a .fbs still shows it, and so
		// the count is visible when one sample turns out to cover far less than
		// the others.
		fields := 0
		for _, t := range schema.Types {
			fields += len(t.Fields)
		}
		fmt.Fprintf(os.Stderr, "%d table(s), %d field(s), from %d buffer(s)\n",
			len(schema.Types), fields, schema.Buffers)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "flatschema:", err)
	os.Exit(2)
}
