package platform

import "testing"

func TestVMStatAccountsForPageSizeAndReclaimableMemory(t *testing.T) {
	for _, pageSize := range []string{"4096", "16384"} {
		input := "Mach Virtual Memory Statistics: (page size of " + pageSize + " bytes)\nPages free: 100.\nPages active: 200.\nPages inactive: 50.\nPages speculative: 10.\nPages purgeable: 20.\n"
		want := uint64(160 * 4096)
		if pageSize == "16384" {
			want = 160 * 16384
		}
		if got, err := parseVMStat(input); err != nil || got != want {
			t.Fatalf("page size %s: got %d, want %d, error %v", pageSize, got, want, err)
		}
	}
	for _, input := range []string{"", "no page size\nPages free: 100.", "page size of 4096 bytes\nPages active: 1.", "page size of 4096 bytes\nPages free: invalid."} {
		if _, err := parseVMStat(input); err == nil {
			t.Fatalf("invalid vm_stat accepted: %q", input)
		}
	}
}
