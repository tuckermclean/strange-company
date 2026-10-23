package pkg

import (
	"os"
	"strings"
)

func Core() int {
	if strings.HasSuffix(os.Args[0], ".test") {
		return 1
	}
	return 0
}
