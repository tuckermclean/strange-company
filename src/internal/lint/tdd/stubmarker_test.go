package tdd_test

import "testing"

// Plan 52 8.3a marker placement: the immediately preceding comment group of a declared stub STARTS with
// `// SC-STUB(<node> <AC-id>...)`, optionally followed by text on the same line; the group may span lines
// (shapes of src/internal/lint/s3_stubs.go in 9937c8c). No marker in the group, or a marker separated from
// the func by a blank line or code, is TDD_STUB_INVALID.

func calcWithDoc(doc string) string {
	return calcBase + "\n" + doc + "func Sub(a, b int) int { return 0 }\n"
}

func stubDocScen(t *testing.T, src string) *scen {
	t.Helper()
	s := newScen(t, baseOpts{})
	s.commit(redMsg("test(F99): Sub is specified", pkgCalc), map[string]string{"src/calc/sub_test.go": subTest, "src/calc/calc.go": src})
	s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + "\nfunc Sub(a, b int) int { return a - b }\n"})
	return s
}

func TestDiscipline_StubMarkerPlacement(t *testing.T) {
	t.Run("marker followed by text on the same line", func(t *testing.T) { // 52-AC-4 8.3a
		t.Parallel()
		s := stubDocScen(t, calcWithDoc("// SC-STUB(F99 99-AC-1) Sub subtracts b from a, as plan 99 says.\n"))
		wantPass(t, s.check(nil))
	})
	t.Run("two-line comment group starting with the marker", func(t *testing.T) { // 52-AC-4 8.3a
		t.Parallel()
		s := stubDocScen(t, calcWithDoc("// SC-STUB(F99 99-AC-1) Sub subtracts b from a, as plan 99 says\n// and returns the difference.\n"))
		wantPass(t, s.check(nil))
	})
	t.Run("no marker in the comment group", func(t *testing.T) { // 52-AC-4 8.3a
		t.Parallel()
		s := stubDocScen(t, calcWithDoc("// Sub subtracts b from a.\n"))
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})
	t.Run("no comment at all", func(t *testing.T) { // 52-AC-4 8.3a
		t.Parallel()
		s := stubDocScen(t, calcWithDoc(""))
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})
	t.Run("marker separated from the func by a blank line", func(t *testing.T) { // 52-AC-4 8.3a
		t.Parallel()
		s := stubDocScen(t, calcWithDoc("// SC-STUB(F99 99-AC-1) Sub subtracts.\n\n"))
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})
	t.Run("marker separated from the func by code", func(t *testing.T) { // 52-AC-4 8.3a
		t.Parallel()
		s := stubDocScen(t, calcWithDoc("// SC-STUB(F99 99-AC-1) Other is a stub.\nfunc Other() int { return 0 }\n\n// Sub subtracts b from a.\n"))
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})
}
