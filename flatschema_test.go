package flatschema

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Mootjelh/flatread"
)

// Fixtures are built here rather than checked in as .bin files. A sample buffer
// is somebody's payload, and the point of this package is that you cannot tell
// what is in one without reading it.

var le = binary.LittleEndian

// scalarTable builds a buffer whose root table holds the given uint32 values at
// the given vtable slots. Slots not named are left out of the table, which is
// what a real buffer does with a field it does not set.
func scalarTable(fields map[uint16]uint32) []byte {
	slots := make([]uint16, 0, len(fields))
	for slot := range fields {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	entries := int(slots[len(slots)-1]-4)/2 + 1
	vtSize := 4 + 2*entries
	tableSize := 4 + 4*len(slots)

	buf := make([]byte, 4+vtSize+tableSize)
	vtPos, tPos := 4, 4+vtSize

	le.PutUint32(buf[0:], uint32(tPos))
	le.PutUint16(buf[vtPos:], uint16(vtSize))
	le.PutUint16(buf[vtPos+2:], uint16(tableSize))
	le.PutUint32(buf[tPos:], uint32(int32(tPos-vtPos)))

	off := 4
	for _, slot := range slots {
		idx := int(slot-4) / 2
		le.PutUint16(buf[vtPos+4+2*idx:], uint16(off))
		le.PutUint32(buf[tPos+off:], fields[slot])
		off += 4
	}
	return buf
}

func infer(t *testing.T, buffers ...[]byte) *Schema {
	t.Helper()
	s, err := Infer(buffers...)
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	return s
}

func root(t *testing.T, s *Schema) *Type {
	t.Helper()
	for _, typ := range s.Types {
		if len(typ.Path) == 0 {
			return typ
		}
	}
	t.Fatal("no root type in the schema")
	return nil
}

// --- inference ---------------------------------------------------------------

func TestInferFindsThePopulatedSlots(t *testing.T) {
	s := infer(t, scalarTable(map[uint16]uint32{4: 4242, 8: 7}))

	r := root(t, s)
	if got := r.Slots(); fmt.Sprint(got) != "[4 8]" {
		t.Errorf("slots = %v, want [4 8]", got)
	}
	if r.Tables != 1 {
		t.Errorf("Tables = %d, want 1", r.Tables)
	}
}

// A slot is only visible when some buffer populates it, which is the whole
// reason this takes several.
func TestInferMergesAcrossBuffers(t *testing.T) {
	only4 := scalarTable(map[uint16]uint32{4: 1})
	only8 := scalarTable(map[uint16]uint32{8: 2})

	s := infer(t, only4, only8)

	r := root(t, s)
	if got := r.Slots(); fmt.Sprint(got) != "[4 8]" {
		t.Errorf("slots = %v, want [4 8]: neither buffer alone shows both", got)
	}
	if r.Tables != 2 {
		t.Errorf("Tables = %d, want 2", r.Tables)
	}
	// A field only half the samples set is worth knowing about, since it says
	// how likely it is to be required.
	if seen := r.Fields[4].Seen; seen != 1 {
		t.Errorf("slot 4 seen in %d of 2 tables, want 1", seen)
	}
}

func TestInferRejectsABufferThatWillNotParse(t *testing.T) {
	_, err := Infer(scalarTable(map[uint16]uint32{4: 1}), []byte{1, 2, 3})
	if err == nil {
		t.Fatal("a buffer that cannot be parsed was accepted")
	}
	// Naming which one matters: inferring from four of five samples silently
	// gives a schema missing whatever only the fifth had.
	if !strings.Contains(err.Error(), "buffer 1") {
		t.Errorf("err = %v, want it to name the buffer", err)
	}
}

func TestInferWithNoBuffers(t *testing.T) {
	if _, err := Infer(); err == nil {
		t.Error("inferring from nothing was accepted")
	}
}

// --- the .fbs output ---------------------------------------------------------

var fieldLine = regexp.MustCompile(`^\s+(\w+): (\S+) \(id: (\d+)\);`)

// tableFields returns the declared fields of each table in the rendered schema,
// in order, as name and id.
func tableFields(t *testing.T, fbs string) map[string][][2]string {
	t.Helper()

	out := map[string][][2]string{}
	current := ""
	for _, line := range strings.Split(fbs, "\n") {
		if strings.HasPrefix(line, "table ") {
			current = strings.TrimSuffix(strings.Fields(line)[1], " {")
			current = strings.TrimSpace(strings.TrimSuffix(current, "{"))
			out[current] = nil
			continue
		}
		if m := fieldLine.FindStringSubmatch(line); m != nil && current != "" {
			out[current] = append(out[current], [2]string{m[1], m[3]})
		}
	}
	return out
}

// The one that has to be right. A field's slot comes from its position in the
// table, so a gap left out moves every field after it and the schema then reads
// the wrong field for the rest of the table. It compiles and it is wrong.
func TestGapsBetweenSlotsBecomePlaceholders(t *testing.T) {
	s := infer(t, scalarTable(map[uint16]uint32{4: 1, 12: 2}))
	fbs := s.FBS()

	fields := tableFields(t, fbs)["Root"]
	if len(fields) != 5 {
		t.Fatalf("Root has %d fields, want 5 for slots 4 through 12\n---\n%s", len(fields), fbs)
	}

	want := []string{"f4", "unused6", "unused8", "unused10", "f12"}
	for i, w := range want {
		if fields[i][0] != w {
			t.Errorf("field %d is %q, want %q\n---\n%s", i, fields[i][0], w, fbs)
		}
	}
}

// flatc rejects a table where the ids are not 0, 1, 2 and so on in order, so
// getting this wrong makes every generated schema uncompilable.
func TestIdsAreContiguousFromZero(t *testing.T) {
	s := infer(t,
		scalarTable(map[uint16]uint32{4: 1, 12: 2}),
		scalarTable(map[uint16]uint32{8: 3}),
	)

	for name, fields := range tableFields(t, s.FBS()) {
		if len(fields) == 0 {
			continue
		}
		for i, f := range fields {
			id, err := strconv.Atoi(f[1])
			if err != nil {
				t.Fatalf("table %s field %s: id %q is not a number", name, f[0], f[1])
			}
			if id != i {
				t.Errorf("table %s: field %d (%s) has id %d, want %d\n---\n%s",
					name, i, f[0], id, i, s.FBS())
			}
		}
	}
}

// A field named for slot 12 has to carry id 4, or the schema reads a different
// field than the buffer wrote.
func TestSlotAndIdAgree(t *testing.T) {
	s := infer(t, scalarTable(map[uint16]uint32{4: 1, 6: 2, 12: 3}))

	for _, f := range tableFields(t, s.FBS())["Root"] {
		var slot int
		if _, err := fmt.Sscanf(strings.TrimLeft(f[0], "funsed"), "%d", &slot); err != nil {
			t.Fatalf("cannot read a slot out of %q", f[0])
		}
		id, _ := strconv.Atoi(f[1])
		if want := (slot - 4) / 2; id != want {
			t.Errorf("%s has id %d, want %d for slot %d", f[0], id, want, slot)
		}
	}
}

// Every type the schema mentions has to be declared in it, or flatc cannot
// resolve the reference.
func TestEveryReferencedTypeIsDeclared(t *testing.T) {
	s := infer(t, scalarTable(map[uint16]uint32{4: 1, 6: 2}))
	fbs := s.FBS()

	declared := map[string]bool{}
	for name := range tableFields(t, fbs) {
		declared[name] = true
	}

	builtin := map[string]bool{
		"uint": true, "[ubyte]": true, "string": true, "ubyte": true,
	}
	for _, line := range strings.Split(fbs, "\n") {
		m := fieldLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		typ := strings.Trim(m[2], "[]")
		if builtin[typ] || builtin["["+typ+"]"] {
			continue
		}
		if !declared[typ] {
			t.Errorf("field %s refers to %q, which the schema never declares\n---\n%s", m[1], typ, fbs)
		}
	}

	if !strings.Contains(fbs, "root_type Root;") {
		t.Errorf("no root_type in the output\n---\n%s", fbs)
	}
}

func TestFBSIsStableAcrossRuns(t *testing.T) {
	buffers := [][]byte{
		scalarTable(map[uint16]uint32{4: 1, 8: 2}),
		scalarTable(map[uint16]uint32{4: 3, 6: 4}),
	}

	first := infer(t, buffers...).FBS()
	for i := 0; i < 5; i++ {
		if got := infer(t, buffers...).FBS(); got != first {
			t.Fatalf("run %d rendered differently; map iteration is leaking into the output", i)
		}
	}
}

func TestNoBuffersLeftUnsaid(t *testing.T) {
	s := infer(t, scalarTable(map[uint16]uint32{4: 1}), scalarTable(map[uint16]uint32{4: 2}))
	if !strings.Contains(s.FBS(), "from 2 sample buffer(s)") {
		t.Error("the output does not say how many samples it came from")
	}
}

// kindTally renders a disagreement between samples, and it walks a map. Ordered
// by Kind rather than by iteration, or the same input renders differently from
// one run to the next and a diff of two schemas is full of noise.
//
// Tested directly because the disagreement path is only reached when samples
// conflict, and TestFBSIsStableAcrossRuns uses samples that agree.
func TestKindTallyIsOrderedNotRandom(t *testing.T) {
	field := &Field{Slot: 4, Kinds: map[flatread.Kind]int{
		flatread.KindVectorOfTables: 1,
		flatread.KindScalar:         3,
		flatread.KindString:         2,
		flatread.KindBytes:          1,
	}}

	first := kindTally(field)
	for i := 0; i < 200; i++ {
		if got := kindTally(field); got != first {
			t.Fatalf("run %d gave %q, first gave %q: map order is reaching the output", i, got, first)
		}
	}

	// Scalar is the lowest Kind, so it leads whatever the map does.
	if !strings.HasPrefix(first, flatread.KindScalar.String()) {
		t.Errorf("tally = %q, want it to start with the lowest Kind", first)
	}
}
