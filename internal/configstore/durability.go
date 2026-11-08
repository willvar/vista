package configstore

import (
	"os"
	"runtime"
)

// Unix needs directory barriers as well as synced file contents before retiring
// migration sources. Windows does not expose Unix directory-fsync semantics;
// files still use File.Sync and replacement follows the native rename contract.
func syncDirectory(root *os.Root, name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
