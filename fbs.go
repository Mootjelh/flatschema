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

	for _, t := range s.Types {
		s.writeTable(&b, t)
	}

	b.WriteString("root_type Root;\n")
	return b.String()
}

// declWidth is how wide a field declaration is padded before its comment, so
// the comments line up in the common case and simply run long in the rest.
const declWidth = 34

func (s *Schema) writeTable(b *strings.Builder, t *Type) {
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
		decl := fmt.Sprintf("f%d: %s (id: %d);", slot, s.fbsType(t, field, kind), id)

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
func (s *Schema) fbsType(t *Type, field *Field, kind flatread.Kind) string {
	switch kind {
	case flatread.KindString:
		return "string"
	case flatread.KindBytes, flatread.KindRoaring:
		return "[ubyte]"
	case flatread.KindTable:
		return s.nestedName(t, field.Slot)
	case flatread.KindVectorOfTables:
		return "[" + s.nestedName(t, field.Slot) + "]"
	default:
		return "uint"
	}
}

// nestedName is the type reached through this slot. It falls back to a byte
// vector when nothing was walked there, which happens when the depth limit cut
// the walk short: naming a table that the file never declares would not compile.
func (s *Schema) nestedName(t *Type, slot uint16) string {
	path := make([]uint16, len(t.Path)+1)
	copy(path, t.Path)
	path[len(t.Path)] = slot

	if nested, ok := s.byPath[pathKey(path)]; ok {
		return nested.Name()
	}
	return "[ubyte]"
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
