package pkg

import "flag"

func Core() int {
	if flag.Lookup("test.v") != nil {
		return 1
	}
	return 0
}
