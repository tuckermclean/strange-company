package crash_test

import (
	"bufio"
	"flag"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The crash matrix runs the engine as a subprocess for every injected crash point. Each run seals the recovery bundle
// with age scrypt at work factor 18 (256 MiB of memory, a second of CPU), next to one SeaweedFS emulator that the whole
// package shares, so the default parallelism depends on the memory available:
//
//	slots = (MemAvailable - reserveMiB) / perRunMiB, clamped to [1, min(GOMAXPROCS, maxSlots)]
//
// An explicit -parallel on the command line wins. When memory cannot be read the tests run one at a time.
const (
	reserveMiB = 3 * 1024
	perRunMiB  = 768
	maxSlots   = 3
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
	n := (availMiB - reserveMiB) / perRunMiB
	if m := runtime.GOMAXPROCS(0); n > m {
		n = m
	}
	if n > maxSlots {
		n = maxSlots
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
	code := m.Run()
	theWorld.shutdown()
	os.Exit(code)
}
