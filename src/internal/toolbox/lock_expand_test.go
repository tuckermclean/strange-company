package toolbox

import "testing"

func TestExpandArchMap(t *testing.T) {
	tool := lockTool{Ver: "1.2.3"}
	cases := []struct{ in, arch, want string }{
		{"https://h/x_{version}_{arch}.tar.gz", "amd64", "https://h/x_1.2.3_amd64.tar.gz"},
		{"https://h/x-{arch:amd64=x86_64,arm64=aarch64}-{version}.zip", "amd64", "https://h/x-x86_64-1.2.3.zip"},
		{"https://h/x-{arch:amd64=x86_64,arm64=aarch64}-{version}.zip", "arm64", "https://h/x-aarch64-1.2.3.zip"},
		{"https://h/{arch:amd64=Linux-64bit,arm64=Linux-ARM64}.tgz", "arm64", "https://h/Linux-ARM64.tgz"},
		{"https://h/{arch:amd64=x64}.tgz", "arm64", "https://h/{arch:amd64=x64}.tgz"},
	}
	for _, c := range cases {
		if got := tool.expand(c.in, c.arch); got != c.want {
			t.Errorf("expand(%q, %s) = %q, want %q", c.in, c.arch, got, c.want)
		}
	}
}
