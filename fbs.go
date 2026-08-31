package flatschema

import (
	"fmt"
	"strings"

	"github.com/Mootjelh/flatread"
)

// FBS renders the schema as a .fbs file.
//
// The grammar is followed and the two rules that would make every output
// uncompilable are pinned by tests, but flatc has not been run against it. See
// the README.
func (s *Schema) FBS() string {
	var b strings.Builder
	r := &render{schema: s, invented: map[string]bool{}}

	fmt.Fprintf(&b, "// Inferred by flatschema from %d sample buffer(s).\n", s.Buffers)
	b.WriteString("//\n")
	b.WriteString("// Names are not in the data, so fields are named for their slot and types\n")
	b.WriteString("// for the route taken to reach them. Scalar widths are not in the data\n")
	b.WriteString("// either; every scalar below is a guess and the comment beside it shows\n")
	b.WriteString("// what was actually there. Rename and retype freely, but see the note on\n")
	b.WriteString("// ids before deleting anything.\n\n")

	if s.Identifier != "" {
		fmt.Fprintf(&b, "file_identifier %q;\n\n", s.Identifier)
	}

	// The tables first, then whatever they turned out to refer to. A reference
	// to a type the walk never reached only comes up while rendering, so the
	// placeholders cannot be written until the body is done.
	var body strings.Builder
	for _, t := range s.Types {
		r.writeTable(&body, t)
	}
	b.WriteString(body.String())
	r.writePlaceholders(&b)

	b.WriteString("root_type Root;\n")
	return b.String()
}

// render is the state one call to [Schema.FBS] builds up: the type names it had
// to invent because the walk never reached them, in the order they came up.
type render struct {
	schema       *Schema
	placeholders []string
	invented     map[string]bool
}

// writePlaceholders declares the types that are referred to above and were
// never walked.
//
// Something has to be declared or the reference dangles. Writing [ubyte] in
// place of the element name is worse than a dangling reference, because it
// makes the field a vector of a vector, and FlatBuffers has no such thing, so
// the file then does not compile at all. An empty table keeps the field the
// shape the buffer says it is, and leaves the fields to a reader with a sample
// that goes deeper.
func (r *render) writePlaceholders(b *strings.Builder) {
	for _, name := range r.placeholders {
		b.WriteString("// Referred to above and never walked, so its fields are unknown.\n")
		fmt.Fprintf(b, "table %s {\n}\n\n", name)
	}
}

// declWidth is how wide a field declaration is padded before its comment, so
// the comments line up in the common case and simply run long in the rest.
const declWidth = 34

func (r *render) writeTable(b *strings.Builder, t *Type) {
	fmt.Fprintf(b, "// Seen %d time(s).\n", t.Tables)
	fmt.Fprintf(b, "table %s {\n", t.Name())

	slots := t.Slots()
	if len(slots) == 0 {
		b.WriteString("  // No populated slots in any sample.\n}\n\n")
		return
	}

	// Every slot from the first to the last, including the ones no sample
	// populated.
	//
	// This is the part that has to be right. A field's slot comes from its
	// position in the table, so leaving a gap out moves every field after it two
	// bytes down and the schema then reads the wrong field for the rest of the
	// table. It compiles, and it is wrong everywhere. The placeholders keep the
	// positions, and the explicit ids make a later edit that removes one fail at
	// compile time rather than silently.
	for slot := uint16(4); slot <= slots[len(slots)-1]; slot += 2 {
		id := (slot - 4) / 2

		field, ok := t.Fields[slot]
		if !ok {
			decl := fmt.Sprintf("unused%d: ubyte (id: %d);", slot, id)
			fmt.Fprintf(b, "  %-*s// no sample populated this slot\n", declWidth, decl)
			continue
		}

		kind, agreed := field.Kind()
		decl := fmt.Sprintf("f%d: %s (id: %d);", slot, r.fbsType(t, field, kind), id)

		if note := comment(field, kind, agreed, t.Tables); note != "" {
			fmt.Fprintf(b, "  %-*s%s\n", declWidth, decl, note)
		} else {
			fmt.Fprintf(b, "  %s\n", decl)
		}
	}

	b.WriteString("}\n\n")
}

// fbsType is the type to write for a field.
//
// A scalar comes out as uint, which is a guess and is labelled as one in the
// comment. Four bytes in a slot is equally a uint, an int, a float, or the
// first four bytes of something the schema knew about and this does not.
func (r *render) fbsType(t *Type, field *Field, kind flatread.Kind) string {
	switch kind {
	case flatread.KindString:
		return "string"
	case flatread.KindBytes, flatread.KindRoaring:
		return "[ubyte]"
	case flatread.KindTable:
		return r.nestedName(t, field.Slot)
	case flatread.KindVectorOfTables:
		return "[" + r.nestedName(t, field.Slot) + "]"
	default:
		return "uint"
	}
}

// nestedName is the type reached through this slot.
//
// The walk does not always get there. It stops at the depth limit, it stops on
// an element that will not read back as a table, and it stops where one table
// is reached twice by different routes. The name is the name either way, so it
// is written and the type is declared empty at the end.
func (r *render) nestedName(t *Type, slot uint16) string {
	path := make([]uint16, len(t.Path)+1)
	copy(path, t.Path)
	path[len(t.Path)] = slot

	if nested, ok := r.schema.byPath[pathKey(path)]; ok {
		return nested.Name()
	}

	name := Type{Path: path}.Name()
	if !r.invented[name] {
		r.invented[name] = true
		r.placeholders = append(r.placeholders, name)
	}
	return name
}

func comment(field *Field, kind flatread.Kind, agreed bool, tables int) string {
	var parts []string

	if kind == flatread.KindScalar {
		parts = append(parts, "width unknown")
	}
	if kind == flatread.KindRoaring {
		parts = append(parts, "looks like a RoaringBitmap")
	}
	if !agreed {
		parts = append(parts, "samples disagreed on the type: "+kindTally(field))
	}
	if field.Seen < tables {
		parts = append(parts, fmt.Sprintf("present in %d of %d", field.Seen, tables))
	}
	if len(field.Samples) > 0 {
		parts = append(parts, strings.Join(field.Samples, ", "))
	}

	if len(parts) == 0 {
		return ""
	}
	return "// " + strings.Join(parts, "; ")
}

func kindTally(field *Field) string {
	var parts []string
	// Ordered by Kind rather than by count, so the same input renders the same
	// way every run.
	for kind := flatread.KindScalar; kind <= flatread.KindVectorOfTables; kind++ {
		if n := field.Kinds[kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s x%d", kind, n))
		}
	}
	return strings.Join(parts, ", ")
}
