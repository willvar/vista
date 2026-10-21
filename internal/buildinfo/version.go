package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

// Version defaults to dev. Release builds set it through the Go linker.
var Version = "dev"

type Metadata struct {
	BinaryHash     string
	SourceCommit   string
	SourceModified bool
}

// Read captures metadata for the running executable. Call it once at startup
// so replacing the binary on disk does not relabel the current process.
func Read() (Metadata, error) {
	var metadata Metadata
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				metadata.SourceCommit = setting.Value
			case "vcs.modified":
				metadata.SourceModified = setting.Value == "true"
			}
		}
	}
	path := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		var err error
		path, err = os.Executable()
		if err != nil {
			return metadata, err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return metadata, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return metadata, err
	}
	metadata.BinaryHash = hex.EncodeToString(hash.Sum(nil))
	return metadata, nil
}
