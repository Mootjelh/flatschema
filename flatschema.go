// Package flatschema infers a FlatBuffers schema from sample buffers.
//
// FlatBuffers data carries no schema. A buffer is a vtable of slot offsets and
// the bytes they point at, and the names and widths of the fields live only in
// the .fbs file that generated it. When you have the data and not the file, the
// usual route is to read the bytes by hand, write down which slot held what,
// and build the schema up one field at a time.
//
// This does that pass for you. It walks one or more buffers, records what each
// slot looks like, merges the observations, and emits a .fbs skeleton that
// flatc will compile.
//
// What comes out is a starting point, not a recovered original, and the reasons
// are in the format rather than in this package:
//
//   - Field names are not in the data at all. Fields come out as f4, f6 and so
//     on, named for their slot.
//   - A scalar's width is not in the data either. A slot holding four bytes is
//     a uint, an int, a float, or four bytes of something else, and only the
//     schema knew which.
//   - A slot is only visible when a buffer populates it. More samples means
//     more of the schema, which is why [Infer] takes several.
//
// The one thing it will not do is guess quietly. Every field that was inferred
// rather than read carries a comment saying what was seen.
package flatschema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Mootjelh/flatread"
)

// MaxDepth is how far nested tables are followed when no depth is given.
const MaxDepth = 12

// Options controls [InferWith].
type Options struct {
	// MaxDepth limits how far nested tables are followed. 0 means [MaxDepth].
	MaxDepth int

	// Elems limits how many elements of a table vector are examined. 0 means
	// all of them.
	//
	// Every element of a vector shares one schema, so the first is usually
	// enough. Reading them all is worth the time anyway: elements populate
	// different slots, and a field only one element sets is a field that would
	// otherwise be missed.
	Elems int
}

func (o Options) maxDepth() int {
	if o.MaxDepth <= 0 {
		return MaxDepth
	}
	return o.MaxDepth
}

// Field is one slot of one table, as it appeared across the samples.
type Field struct {
	// Slot is the vtable slot, counting from 4 in steps of 2.
	Slot uint16

	// Kinds is what the slot looked like, and how often. More than one entry
	// means the samples disagreed, which is worth a human deciding rather than
	// this package picking.
	Kinds map[flatread.Kind]int

	// Seen is how many tables of this type populated the slot, out of Tables on
	// the type. A slot every sample sets is likelier to be required than one
	// that showed up once.
	Seen int

	// Samples holds a few rendered values, for the comment in the output. It is
	// how a reader recognises a field that this package can only call f12.
	Samples []string
}

// Kind is the kind seen most often, and whether the samples agreed on it.
func (f Field) Kind() (flatread.Kind, bool) {
	best, bestCount, total := flatread.KindScalar, -1, 0
	for kind, count := range f.Kinds {
		total += count
		// Ties break towards the lower Kind so the output does not move between
		// runs over the same input.
		if count > bestCount || (count == bestCount && kind < best) {
			best, bestCount = kind, count
		}
	}
	return best, bestCount == total
}

// Type is one table shape, identified by where it sits rather than by a name,
// since the buffer holds no names.
//
// Two tables are the same type when they are reached by the same route from the
// root: the table in root slot 10 is one type, and the tables in the vector in
// root slot 12 are another. Element indices are not part of the route, because
// every element of a vector shares a schema.
type Type struct {
	// Path is the slot route from the root. The root itself has none.
	Path []uint16

	// Tables counts how many tables of this type were walked.
	Tables int

	// Fields is keyed by slot.
	Fields map[uint16]*Field
}

// Name is the generated type name: Root, then the slots it took to get here.
func (t Type) Name() string {
	if len(t.Path) == 0 {
		return "Root"
	}
	parts := make([]string, len(t.Path))
	for i, slot := range t.Path {
		parts[i] = fmt.Sprint(slot)
	}
	return "T" + strings.Join(parts, "_")
}

// Schema is what was inferred.
type Schema struct {
	// Types in the order they were first reached, so the root comes first and
	// the output reads top down.
	Types []*Type

	// Buffers is how many samples went in, and Identifier the file identifier
	// if the samples carried one and agreed on it.
	Buffers    int
	Identifier string

	byPath map[string]*Type
}

// Infer reads the buffers and returns the schema they imply.
func Infer(buffers ...[]byte) (*Schema, error) {
	return InferWith(Options{}, buffers...)
}

// InferWith is [Infer] with control over depth and breadth.
//
// A buffer that will not parse is an error naming which one it was, rather than
// a silent skip. Inferring a schema from four of five samples, without saying
// so, produces a schema missing whatever only the fifth had.
func InferWith(opts Options, buffers ...[]byte) (*Schema, error) {
	if len(buffers) == 0 {
		return nil, fmt.Errorf("flatschema: no buffers given")
	}

	s := &Schema{byPath: map[string]*Type{}}
	identifiers := map[string]bool{}

	for i, buf := range buffers {
		payload := buf
		if flatread.IsSizePrefixed(buf) {
			payload = buf[4:]
		}
		if id, ok := flatread.FileIdentifier(payload); ok {
			identifiers[id] = true
		}

		root, err := rootOf(buf)
		if err != nil {
			return nil, fmt.Errorf("flatschema: buffer %d: %w", i, err)
		}

		s.walk(root, nil, 1, opts, map[uint32]bool{})
		s.Buffers++
	}

	// Only reported when every sample that had one agreed. Two different
	// identifiers mean these are not samples of one schema, and saying nothing
	// is better than picking one.
	if len(identifiers) == 1 {
		for id := range identifiers {
			s.Identifier = id
		}
	}
	return s, nil
}

func rootOf(buf []byte) (flatread.Table, error) {
	if flatread.IsSizePrefixed(buf) {
		return flatread.RootSizePrefixed(buf)
	}
	return flatread.Root(buf)
}

func (s *Schema) walk(t flatread.Table, path []uint16, depth int, opts Options, seen map[uint32]bool) {
	// The seen set guards against an offset cycle, which a corrupt or hostile
	// buffer can contain and which would otherwise not terminate.
	if depth > opts.maxDepth() || seen[t.Pos()] {
		return
	}
	seen[t.Pos()] = true

	typ := s.typeFor(path)
	typ.Tables++

	for _, slot := range t.Slots() {
		field := typ.field(slot)
		field.Seen++

		kind := t.Guess(slot)
		field.Kinds[kind]++
		field.addSample(describe(t, slot, kind))

		here := make([]uint16, len(path)+1)
		copy(here, path)
		here[len(path)] = slot

		switch kind {
		case flatread.KindTable:
			if sub, ok := t.Table(slot); ok {
				s.walk(sub, here, depth+1, opts, seen)
			}
		case flatread.KindVectorOfTables:
			n := t.VectorLen(slot)
			if opts.Elems > 0 && n > opts.Elems {
				n = opts.Elems
			}
			for i := 0; i < n; i++ {
				if sub, ok := t.TableAt(slot, i); ok {
					s.walk(sub, here, depth+1, opts, seen)
				}
			}
		}
	}
}

func (s *Schema) typeFor(path []uint16) *Type {
	key := pathKey(path)
	if t, ok := s.byPath[key]; ok {
		return t
	}

	t := &Type{Path: append([]uint16(nil), path...), Fields: map[uint16]*Field{}}
	s.byPath[key] = t
	s.Types = append(s.Types, t)
	return t
}

func (t *Type) field(slot uint16) *Field {
	if f, ok := t.Fields[slot]; ok {
		return f
	}
	f := &Field{Slot: slot, Kinds: map[flatread.Kind]int{}}
	t.Fields[slot] = f
	return f
}

// Slots returns this type's slots in order.
func (t Type) Slots() []uint16 {
	out := make([]uint16, 0, len(t.Fields))
	for slot := range t.Fields {
		out = append(out, slot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// maxSamples is how many example values a field keeps. Enough to recognise a
// field by, short enough to stay on one line.
const maxSamples = 3

func (f *Field) addSample(v string) {
	if v == "" || len(f.Samples) >= maxSamples {
		return
	}
	for _, have := range f.Samples {
		if have == v {
			return
		}
	}
	f.Samples = append(f.Samples, v)
}

// describe renders a short example of what a slot held, for the comment beside
// the generated field. It is the only thing in the output that can tell a
// reader which field f12 really is.
func describe(t flatread.Table, slot uint16, kind flatread.Kind) string {
	switch kind {
	case flatread.KindString:
		s := t.String(slot)
		if len(s) > 40 {
			s = s[:40] + "..."
		}
		return fmt.Sprintf("%q", s)
	case flatread.KindBytes, flatread.KindRoaring:
		return fmt.Sprintf("%d bytes", len(t.Bytes(slot)))
	case flatread.KindVectorOfTables:
		return fmt.Sprintf("%d elements", t.VectorLen(slot))
	case flatread.KindTable:
		return ""
	default:
		// Every width, because which one the schema meant is exactly what is
		// missing. A reader who recognises the number recognises the field.
		return fmt.Sprintf("u32=%d u16=%d u8=%d", t.Uint32(slot), t.Uint16(slot), t.Byte(slot))
	}
}

func pathKey(path []uint16) string {
	parts := make([]string, len(path))
	for i, slot := range path {
		parts[i] = fmt.Sprint(slot)
	}
	return strings.Join(parts, ".")
}
