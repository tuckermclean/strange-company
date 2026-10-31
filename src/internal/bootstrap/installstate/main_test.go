package installstate_test

import (
	"bufio"
	"flag"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The install tests are parallel (see TestInstallTests_AreParallel), but each
// recovery-bundle seal or open derives a scrypt key with a 2^18 cost parameter
// (256 MiB; several GiB of footprint under -race), and the identity package's
// work-factor-20 test needs about 5 GiB while it runs next to this package.
// Unbounded parallelism killed the test binary on a 7.7 GiB host, so the
// default parallelism depends on the memory available:
//
//	slots = (MemAvailable - reserveMiB) / perTestMiB, clamped to [1, GOMAXPROCS]
//
// An explicit -parallel on the command line wins. When memory cannot be read
// the tests run one at a time.
const (
	reserveMiB = 6 * 1024
	perTestMiB = 2 * 1024
)

func availableMiB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kib, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0
			}
			return kib / 1024
		}
	}
	return 0
}

func parallelSlots(availMiB int) int {
	n := (availMiB - reserveMiB) / perTestMiB
	if m := runtime.GOMAXPROCS(0); n > m {
		n = m
	}
	if n < 1 {
		n = 1
	}
	return n
}

func TestMain(m *testing.M) {
	flag.Parse()
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "test.parallel" {
			explicit = true
		}
	})
	if !explicit {
		_ = flag.Set("test.parallel", strconv.Itoa(parallelSlots(availableMiB())))
	}
	os.Exit(m.Run())
}
