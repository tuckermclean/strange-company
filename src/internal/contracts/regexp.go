package contracts

import (
	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ecmaRegexp adapts regexp2 in ECMAScript mode to the validator: JSON Schema patterns are ECMA-262 regular
// expressions and the contracts use lookahead, which Go's RE2 engine rejects.
type ecmaRegexp struct{ re *regexp2.Regexp }

func (e ecmaRegexp) MatchString(s string) bool {
	ok, err := e.re.MatchString(s)
	return err == nil && ok
}

func (e ecmaRegexp) String() string { return e.re.String() }

func compileECMA(s string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(s, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return ecmaRegexp{re}, nil
}
