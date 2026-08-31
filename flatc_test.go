package flatschema

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The generated schema is only worth anything if flatc takes it, and until this
// existed nobody had asked flatc. The tests elsewhere pin the two rules that
// would break every output, which is not the same as compiling one.
//
// flatc is not a Go dependency and will not be on most machines, so this skips
// when it is missing. A check that skips is a check that passes for the wrong
// reason, so CI sets FLATSCHEMA_REQUIRE_FLATC and the skip becomes a failure
// there.
func flatc(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("flatc")
	if err == nil {
		return path
	}
	if os.Getenv("FLATSCHEMA_REQUIRE_FLATC") != "" {
		t.Fatalf("FLATSCHEMA_REQUIRE_FLATC is set and flatc is not on PATH: %v", err)
	}
	t.Skip("flatc is not on PATH; install it to compile the generated schemas")
	return ""
}

// emptyRoot builds a buffer whose root table declares no slots. It is what an
// all-defaults table looks like, and it reaches the branch that writes a table
// body with nothing in it.
func emptyRoot() []byte {
	buf := make([]byte, 16)
	le.PutUint32(buf[0:], 8)
	le.PutUint16(buf[4:], 4) // vtable: header only, no slots
	le.PutUint16(buf[6:], 4) // table size
	le.PutUint32(buf[8:], uint32(int32(8-4)))
	return buf
}

func TestFlatcCompilesTheGeneratedSchema(t *testing.T) {
	bin := flatc(t)

	cases := []struct {
		name   string
		opts   Options
		buffer [][]byte
	}{
		{"scalars", Options{}, [][]byte{scalarTable(map[uint16]uint32{4: 4242, 8: 7})}},
		// Gaps become placeholder fields, which carry ids of their own.
		{"gaps", Options{}, [][]byte{scalarTable(map[uint16]uint32{4: 1, 12: 2})}},
		// Samples that disagree put a tally in the comment.
		{"merged", Options{}, [][]byte{
			scalarTable(map[uint16]uint32{4: 1, 12: 2}),
			scalarTable(map[uint16]uint32{8: 3}),
		}},
		{"vector of tables", Options{}, [][]byte{tableVector(11, 22)}},
		// The walk stops before the elements, so the element type is invented
		// and has to be declared. This is the case that did not compile.
		{"vector of tables, walk cut short", Options{MaxDepth: 1}, [][]byte{tableVector(11, 22)}},
		{"table with no fields", Options{}, [][]byte{emptyRoot()}},
		{"everything at once", Options{}, [][]byte{
			scalarTable(map[uint16]uint32{4: 1, 12: 2}),
			tableVector(11, 22),
			emptyRoot(),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := InferWith(tc.opts, tc.buffer...)
			if err != nil {
				t.Fatalf("InferWith: %v", err)
			}
			fbs := s.FBS()

			dir := t.TempDir()
			path := filepath.Join(dir, "schema.fbs")
			if err := os.WriteFile(path, []byte(fbs), 0o644); err != nil {
				t.Fatalf("writing the schema: %v", err)
			}

			out, err := exec.Command(bin, "--cpp", "-o", dir, path).CombinedOutput()
			if err != nil {
				t.Errorf("flatc rejected the schema: %v\n%s\n---\n%s", err, out, fbs)
			}
		})
	}
}

// The control for the test above. If flatc accepted anything, or the command
// were being run in a way that cannot report failure, every case would pass and
// prove nothing.
func TestFlatcRejectsASchemaItShould(t *testing.T) {
	bin := flatc(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "broken.fbs")
	broken := "table Root {\n  f4: [[ubyte]] (id: 0);\n}\nroot_type Root;\n"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("writing the schema: %v", err)
	}

	out, err := exec.Command(bin, "--cpp", "-o", dir, path).CombinedOutput()
	if err == nil {
		t.Fatal("flatc accepted a nested vector, so the check above proves nothing")
	}
	if !strings.Contains(string(out), "nested vector") {
		t.Errorf("flatc failed for some other reason than the one expected:\n%s", out)
	}
}
