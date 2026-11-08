// Package configstore owns Vista's private, sectioned configuration document.
// It stores raw user settings, never resolved catalogs or public API projections.
package configstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/platform"
)

const Filename = "config.json"
const Version = 2
const LegacyVersion = 1
const Server = "server"
const Capacity = "capacity"
const Agent = "agent"
const Search = "search"
const UserAgent = "userAgent"
const Models = "models"
const MCP = "mcp"
const maxDocumentBytes = 1 << 20

var ErrConflict = errors.New("configuration changed; read it again before saving")
var ErrNeedsMigration = errors.New("start Vista to migrate existing configuration before accessing settings")

type binding struct {
	section, legacy string
	limit           int
}

var bindings = []binding{
	{Server, "server.json", 4 << 10}, {Capacity, "capacity.json", 4 << 10},
	{Agent, "agent-settings.json", 4 << 10}, {Search, "search.json", 256 << 10},
	{UserAgent, "user-agent.json", 4 << 10}, {Models, "models.json", 32 << 10}, {MCP, "mcp.json", 8 << 10},
}

type document struct {
	Version   int             `json:"version"`
	Server    json.RawMessage `json:"server,omitempty"`
	Capacity  json.RawMessage `json:"capacity,omitempty"`
	Agent     json.RawMessage `json:"agent,omitempty"`
	Search    json.RawMessage `json:"search,omitempty"`
	UserAgent json.RawMessage `json:"userAgent,omitempty"`
	Models    json.RawMessage `json:"models,omitempty"`
	MCP       json.RawMessage `json:"mcp,omitempty"`
}

func (d *document) field(section string) *json.RawMessage {
	switch section {
	case Server:
		return &d.Server
	case Capacity:
		return &d.Capacity
	case Agent:
		return &d.Agent
	case Search:
		return &d.Search
	case UserAgent:
		return &d.UserAgent
	case Models:
		return &d.Models
	case MCP:
		return &d.MCP
	}
	return nil
}
func sectionBinding(section string) (binding, error) {
	for _, b := range bindings {
		if b.section == section {
			return b, nil
		}
	}
	return binding{}, errors.New("unknown configuration section")
}
func compact(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("configuration must be UTF-8 JSON")
	}
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		return nil, errors.New("invalid configuration JSON")
	}
	return out.Bytes(), nil
}
func decode(data []byte) (*document, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("config.json must be one JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return nil, errors.New("duplicate or invalid configuration field")
		}
		if name != "version" {
			if _, err := sectionBinding(name); err != nil {
				return nil, errors.New("unknown config.json field")
			}
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("invalid configuration JSON")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("config.json must be one JSON object")
	}
	if !seen["version"] || !utf8.Valid(data) {
		return nil, errors.New("config.json version is required")
	}
	var d document
	if err := json.Unmarshal(data, &d); err != nil || d.Version != Version && d.Version != LegacyVersion {
		return nil, errors.New("unsupported config.json version")
	}
	for _, b := range bindings {
		field := d.field(b.section)
		if len(*field) == 0 {
			continue
		}
		value, err := compact(*field)
		if err != nil {
			return nil, fmt.Errorf("invalid %s configuration section", b.section)
		}
		*field = value
	}
	if d.Version == Version {
		if err := validatePublicDocument(&d); err != nil {
			return nil, err
		}
	}
	return &d, nil
}

// Configuration JSON is stored as data, never embedded into HTML. Keep raw
// section strings from expanding at migration/read boundaries solely due to
// HTML escaping, so the same bounded logical section remains readable.
func marshalConfiguration(value any, indent bool) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if indent {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if indent {
		return out.Bytes(), nil
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Revision covers one compact section, so an unrelated section's save does not
// create a conflict. Missing is distinct from an explicit null or empty object.
func Revision(data []byte, present bool) string {
	if !present {
		return "missing"
	}
	if normalized, err := compact(data); err == nil {
		return digest(normalized)
	}
	return digest(data)
}

func openRoot(dir string, create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || create && !platform.PrivateMode(before.Mode()) {
		return nil, errors.New("Vista configuration directory must be private")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) || create && !platform.PrivateMode(after.Mode()) {
		root.Close()
		return nil, errors.New("configuration directory changed")
	}
	return root, nil
}
func readFile(root *os.Root, name string, limit int, private bool) ([]byte, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || private && !platform.PrivateMode(before.Mode()) || before.Size() > int64(limit) {
		return nil, nil, errors.New("configuration must be a private regular file within its size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) || !opened.Mode().IsRegular() || private && !platform.PrivateMode(opened.Mode()) {
		return nil, nil, ErrConflict
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, nil, errors.New("could not read configuration within its size limit")
	}
	after, err := file.Stat()
	if err != nil || !sameInfo(opened, after) {
		return nil, nil, ErrConflict
	}
	return data, opened, nil
}
func sameInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

type cached struct {
	root os.FileInfo
	hash string
	doc  *document
}

var snapshots = struct {
	sync.Mutex
	values map[string]cached
}{values: map[string]cached{}}

func readDocument(root *os.Root, key string, useCache bool) (*document, []byte, bool, error) {
	info, err := root.Lstat(Filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if !info.Mode().IsRegular() || !platform.PrivateMode(info.Mode()) || info.Size() > maxDocumentBytes {
		return nil, nil, false, errors.New("config.json must be a private regular file of at most 1 MiB")
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return nil, nil, false, err
	}
	if !platform.PrivateMode(rootInfo.Mode()) {
		return nil, nil, false, errors.New("Vista configuration directory must be private")
	}
	// Verify actual bytes before reusing decoded sections: editors may preserve
	// timestamps and file size while changing content in place.
	var data []byte
	for attempt := 0; attempt < 3; attempt++ {
		data, _, err = readFile(root, Filename, maxDocumentBytes, true)
		if !errors.Is(err, ErrConflict) {
			break
		}
	}
	if err != nil {
		return nil, nil, false, err
	}
	hash := digest(data)
	if useCache {
		snapshots.Lock()
		value, ok := snapshots.values[key]
		snapshots.Unlock()
		if ok && os.SameFile(value.root, rootInfo) && value.hash == hash {
			return value.doc, data, true, nil
		}
	}
	doc, err := decode(data)
	if err != nil {
		return nil, nil, false, err
	}
	if useCache {
		snapshots.Lock()
		if len(snapshots.values) >= 16 {
			clear(snapshots.values)
		}
		snapshots.values[key] = cached{rootInfo, hash, doc}
		snapshots.Unlock()
	}
	return doc, data, true, nil
}
func invalidate(key string) { snapshots.Lock(); delete(snapshots.values, key); snapshots.Unlock() }

// Read never changes files or reads legacy setting values. Startup migration
// must finish before existing old-format data can be accessed.
func Read(dir, section string) ([]byte, bool, error) {
	b, err := sectionBinding(section)
	if err != nil {
		return nil, false, err
	}
	key, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, err
	}
	root, err := openRoot(key, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	doc, mainData, exists, err := readDocument(root, key, true)
	if err != nil {
		return nil, false, err
	}
	if exists {
		if doc.Version != Version {
			return nil, false, ErrNeedsMigration
		}
		value, err := resolvedSection(root, key, doc, mainData, section)
		if err != nil {
			return nil, false, err
		}
		if len(value) > b.limit {
			return nil, false, fmt.Errorf("oversized %s configuration section", section)
		}
		return bytes.Clone(value), len(value) != 0, nil
	}
	if pending, err := unmigratedInputs(root); err != nil {
		return nil, false, err
	} else if pending {
		return nil, false, ErrNeedsMigration
	}
	return nil, false, nil
}

func lock(root *os.Root) (*os.File, error) {
	if err := root.Mkdir("state", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := root.Lstat("state")
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return nil, errors.New("configuration state directory must be private")
	}
	file, err := root.OpenFile("state/config.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	current, pathErr := root.Lstat("state/config.lock")
	if err != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) || !platform.PrivateMode(opened.Mode()) {
		file.Close()
		return nil, errors.New("invalid configuration lock")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := platform.TryLock(file); err == nil {
			return file, nil
		} else if !platform.IsLockBusy(err) {
			file.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			file.Close()
			return nil, errors.New("configuration is busy; retry the change")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func writePrivate(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}
func commit(root *os.Root, doc *document, original []byte, existed bool, checks ...func() error) (bool, error) {
	data, err := marshalConfiguration(doc, true)
	if err != nil {
		return false, err
	}
	if len(data) > maxDocumentBytes {
		return false, errors.New("config.json exceeds 1 MiB")
	}
	if _, err := decode(data); err != nil {
		return false, err
	}
	return commitFile(root, Filename, data, original, existed, checks...)
}
func verifySnapshot(root *os.Root, filename string, original []byte, existed bool) error {
	current, _, err := readFile(root, filename, maxDocumentBytes, true)
	if errors.Is(err, os.ErrNotExist) {
		if existed {
			return ErrConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !existed || digest(current) != digest(original) {
		return ErrConflict
	}
	return nil
}
func commitFile(root *os.Root, filename string, data, original []byte, existed bool, checks ...func() error) (bool, error) {
	name := ".config-" + uuid.NewString()
	defer root.Remove(name)
	if err := writePrivate(root, name, data); err != nil {
		return false, err
	}
	if err := verifySnapshot(root, filename, original, existed); err != nil {
		return false, err
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return false, err
		}
	}
	if err := root.Rename(name, filename); err != nil {
		return false, err
	}
	return true, syncDirectory(root, ".")
}

// Update serializes cooperating writers across Server and helper processes.
// The callback must only transform its section; nil removes it, not the document.
func Update(dir, section string, change func([]byte, bool) ([]byte, error)) error {
	b, err := sectionBinding(section)
	if err != nil {
		return err
	}
	key, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err := openRoot(key, true)
	if err != nil {
		return err
	}
	defer root.Close()
	guard, err := lock(root)
	if err != nil {
		return err
	}
	defer guard.Close()
	state, err := loadState(root, key)
	if err != nil {
		return err
	}
	doc := state.raw
	current := *doc.field(section)
	next, err := change(bytes.Clone(current), len(current) != 0)
	if err != nil {
		return err
	}
	if next != nil {
		next, err = compact(next)
		if err != nil || len(next) > b.limit {
			return fmt.Errorf("invalid or oversized %s configuration section", section)
		}
	}
	if state.existed && state.public.Version == Version && state.keys.exists && bytes.Equal(current, next) {
		return nil
	}
	*doc.field(section) = bytes.Clone(next)
	return completePairCommit(root, key, state)
}
func Write(dir, section string, data []byte) error {
	return Update(dir, section, func([]byte, bool) ([]byte, error) { return data, nil })
}
func CompareAndSwap(dir, section, revision string, data []byte) error {
	return Update(dir, section, func(current []byte, present bool) ([]byte, error) {
		if Revision(current, present) != revision {
			return nil, ErrConflict
		}
		return data, nil
	})
}
