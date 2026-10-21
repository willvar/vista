package platform

import "testing"

func TestOSReleaseName(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"pretty name", "NAME=Ubuntu\nVERSION_ID=24.04\nPRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\n", "Ubuntu 24.04.3 LTS"},
		{"rolling release", "NAME=\"Arch Linux\"\nPRETTY_NAME=\"Arch Linux\"\nBUILD_ID=rolling\n", "Arch Linux"},
		{"name and version", "# fallback\nNAME='Example Linux'\nVERSION_ID=12\n", "Example Linux 12"},
		{"empty pretty name", "PRETTY_NAME=\"\"\nNAME=Example\nVERSION_ID=1\n", "Example 1"},
		{"shell escapes", "PRETTY_NAME=\"Example \\\"Linux\\\" \\$OS\"\n", "Example \"Linux\" $OS"},
		{"unavailable", "# no release metadata\nID=example\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := osReleaseName(tc.data); got != tc.want {
				t.Fatalf("release name = %q, want %q", got, tc.want)
			}
		})
	}
}
