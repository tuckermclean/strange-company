package tdd_test

import (
	"strings"
	"testing"
)

// CD-59, plan 52 8.3a: a declared stub may be a field added to an EXISTING struct type, when the field is
// directly preceded by its own `// SC-STUB(<node> <AC-id>...)` marker and nothing else in that declaration
// changes. The shape is the Options/Report change of 4cce571 in src/internal/lint/tdd/tdd.go.
// Choice for interfaces: only struct fields are allowed. A method added to an existing interface is a change
// of an existing declaration and is TDD_STUB_INVALID, marker or not (CD-59 says "a field added to an existing
// struct type"; an interface method carries behavior a stub cannot supply).

const optsBase = `package calc

// Opts configures calc.
type Opts struct {
	Name string
	Mode int
}

// Shape is something with an area.
type Shape interface {
	Area() int
}
`

const subStubFunc = "\n// SC-STUB(F99 99-AC-1)\nfunc Sub(a, b int) int { return 0 }\n"
const subRealFunc = "\nfunc Sub(a, b int) int { return a - b }\n"

// fieldScen puts opts.go (optsBase) on main, then a RED commit that rewrites it to redOpts (plus the Sub
// func stub and its test), then a HEAD commit that rewrites it to headOpts and implements Sub. extra adds
// further files to the RED commit.
func fieldScen(t *testing.T, redOpts, headOpts string, extra map[string]string) *scen {
	t.Helper()
	s := newScen(t, baseOpts{})
	s.git("checkout", "-q", "main")
	s.commit("chore: opts\n", map[string]string{"src/calc/opts.go": optsBase})
	s.git("checkout", "-q", "-B", "feature/x")
	files := map[string]string{
		"src/calc/sub_test.go": subTest,
		"src/calc/calc.go":     calcBase + subStubFunc,
		"src/calc/opts.go":     redOpts,
	}
	for k, v := range extra {
		files[k] = v
	}
	s.commit(redMsg("test(F99): Sub is specified", pkgCalc), files)
	s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + subRealFunc, "src/calc/opts.go": headOpts})
	return s
}

func withFields(fields string) string {
	return strings.Replace(optsBase, "\tMode int\n", "\tMode int\n"+fields, 1)
}

func withoutMarkers(src string) string {
	var out []string
	for _, l := range strings.Split(src, "\n") {
		if !strings.Contains(l, "SC-STUB(") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestDiscipline_FieldStubs_Valid(t *testing.T) { // 52-AC-4 8.3a CD-59
	cases := map[string]string{
		"one field": "\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n",
		"several fields": "\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n" +
			"\t// SC-STUB(F01 52-AC-4)\n\tOther string\n" +
			"\t// SC-STUB(F01 52-AC-4)\n\tCount int\n",
		"struct tags": "\t// SC-STUB(F01 52-AC-4)\n\tExtra bool `json:\"extra\"`\n" +
			"\t// SC-STUB(F01 52-AC-4)\n\tSource string `json:\"source,omitempty\"`\n",
		"marker with trailing text": "\t// SC-STUB(F01 52-AC-4) Extra is set by the Runner (CD-58b).\n\tExtra bool\n",
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			red := withFields(fields)
			head := withFields(withoutMarkers(fields))
			wantPass(t, fieldScen(t, red, head, nil).check(nil))
		})
	}
	t.Run("field inserted between existing fields", func(t *testing.T) {
		t.Parallel()
		red := strings.Replace(optsBase, "\tName string\n", "\tName string\n\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n", 1)
		wantPass(t, fieldScen(t, red, withoutMarkers(red), nil).check(nil))
	})
	t.Run("field stub with a function stub for a method of the type", func(t *testing.T) {
		t.Parallel()
		red := withFields("\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n") +
			"\n// SC-STUB(F01 52-AC-4)\nfunc (o Opts) Label() string { return \"\" }\n"
		head := withFields("\tExtra bool\n") + "\nfunc (o Opts) Label() string { return o.Name }\n"
		wantPass(t, fieldScen(t, red, head, nil).check(nil))
	})
}

func TestDiscipline_FieldStubs_Invalid(t *testing.T) { // 52-AC-4 8.3a CD-59
	const good = "\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n"
	cases := map[string]string{
		"added field without a marker":                    withFields("\tExtra bool\n"),
		"added field with a plain comment":                withFields("\t// Extra is new.\n\tExtra bool\n"),
		"marked field next to an unmarked added field":    withFields(good + "\tOther string\n"),
		"marker separated from the field by a blank line": withFields("\t// SC-STUB(F01 52-AC-4)\n\n\tExtra bool\n"),
		"existing field modified":                         strings.Replace(withFields(good), "Mode int\n", "Mode int64\n", 1),
		"existing field removed":                          strings.Replace(withFields(good), "\tMode int\n", "", 1),
		"existing field renamed":                          strings.Replace(withFields(good), "\tName string\n", "\tTitle string\n", 1),
		"type renamed":                                    strings.Replace(withFields(good), "type Opts struct", "type Options struct", 1),
		"type kind changed":                               "package calc\n\n// Opts configures calc.\ntype Opts interface {\n\tName() string\n}\n" + optsBase[strings.Index(optsBase, "\n// Shape"):],
		"method added without a marker":                   withFields(good) + "\nfunc (o Opts) Label() string { return \"\" }\n",
		"method added with a non-zero body":               withFields(good) + "\n// SC-STUB(F01 52-AC-4)\nfunc (o Opts) Label() string { return o.Name }\n",
		"interface method added with a marker":            strings.Replace(optsBase, "\tArea() int\n", "\tArea() int\n\t// SC-STUB(F01 52-AC-4)\n\tPerimeter() int\n", 1),
		"interface method added without a marker":         strings.Replace(optsBase, "\tArea() int\n", "\tArea() int\n\tPerimeter() int\n", 1),
	}
	for name, red := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wantFailHas(t, fieldScen(t, red, withFields("\tExtra bool\n"), nil).check(nil), "TDD_STUB_INVALID")
		})
	}
	t.Run("field added by a commit that also modifies a line of the same declaration", func(t *testing.T) {
		t.Parallel()
		red := strings.Replace(withFields(good), "// Opts configures calc.", "// Opts configures the calc package.", 1)
		wantFailHas(t, fieldScen(t, red, withFields("\tExtra bool\n"), nil).check(nil), "TDD_STUB_INVALID")
	})
}

func TestDiscipline_FieldStubs_NotReplaced(t *testing.T) { // 52-AC-4 8.3a CD-59
	t.Run("field marker remains at HEAD", func(t *testing.T) {
		t.Parallel()
		red := withFields("\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n")
		wantFail(t, fieldScen(t, red, red, nil).check(nil), "TDD_STUB_NOT_REPLACED")
	})
	t.Run("one of two field markers remains at HEAD", func(t *testing.T) {
		t.Parallel()
		red := withFields("\t// SC-STUB(F01 52-AC-4)\n\tExtra bool\n\t// SC-STUB(F01 52-AC-4)\n\tOther string\n")
		head := withFields("\tExtra bool\n\t// SC-STUB(F01 52-AC-4)\n\tOther string\n")
		wantFail(t, fieldScen(t, red, head, nil).check(nil), "TDD_STUB_NOT_REPLACED")
	})
}
