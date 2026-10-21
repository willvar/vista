package instance

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"vista/internal/appdirs"
	"vista/internal/platform"
)

type Info struct {
	PID     int    `json:"pid"`
	Address string `json:"address"`
}

const maxInfoBytes = 4096

func Dir() (string, error) {
	return appdirs.State()
}
func Read(dir string) (Info, error) {
	var x Info
	file, err := os.Open(filepath.Join(dir, "instance.json"))
	if err != nil {
		return x, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, maxInfoBytes+1))
	if err != nil {
		return x, err
	}
	if len(b) > maxInfoBytes {
		return x, errors.New("Vista instance record exceeds 4 KiB")
	}
	err = json.Unmarshal(b, &x)
	return x, err
}
func Write(dir string, x Info) error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	if len(b) > maxInfoBytes {
		return errors.New("Vista instance record exceeds 4 KiB")
	}
	tmp, err := os.CreateTemp(dir, "instance-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "instance.json"))
}
func Lock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = platform.TryLock(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func Unlock(f *os.File) error { return f.Close() }
func IsAlive(pid int) bool    { return platform.IsAlive(pid) }
func IsLoopbackAddress(addr string) bool {
	return strings.HasPrefix(addr, "http://127.0.0.1:") || strings.HasPrefix(addr, "http://[::1]:")
}
func Port(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return ""
	}
	p := strings.TrimRight(addr[i+1:], "/")
	if _, err := strconv.Atoi(p); err != nil {
		return ""
	}
	return p
}
