package configstore

import (
	"bytes"
	"errors"
	"log"
	"os"
)

type keySnapshot struct {
	doc    *keyDocument
	data   []byte
	exists bool
}

type storedState struct {
	public   *document
	raw      *document
	original []byte
	existed  bool
	keys     keySnapshot
	legacy   []legacyFile
}

func readKeys(root *os.Root) (keySnapshot, error) {
	var data []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		data, _, err = readFile(root, KeyFilename, maxDocumentBytes, true)
		if !errors.Is(err, ErrConflict) {
			break
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return keySnapshot{doc: emptyKeys()}, nil
	}
	if err != nil {
		return keySnapshot{}, err
	}
	doc, err := decodeKeys(data)
	if err != nil {
		return keySnapshot{}, err
	}
	return keySnapshot{doc: doc, data: data, exists: true}, nil
}

func cloneDocument(doc *document) (*document, error) {
	clone := *doc
	for _, binding := range bindings {
		*clone.field(binding.section) = bytes.Clone(*doc.field(binding.section))
	}
	return &clone, nil
}

func loadStartupState(root *os.Root, key string) (*storedState, error) {
	public, original, existed, err := readDocument(root, key, false)
	if err != nil {
		return nil, err
	}
	state := &storedState{public: public, original: original, existed: existed}
	if !existed {
		state.public, state.legacy, err = collectLegacy(root)
		if err != nil {
			return nil, err
		}
	}
	state.keys, err = readKeys(root)
	if err != nil {
		return nil, err
	}
	if state.public.Version == Version {
		if !state.keys.exists {
			return nil, errors.New("key.json is required for current configuration")
		}
		state.raw, err = resolveDocument(state.public, state.keys.doc)
	} else {
		state.raw, err = cloneDocument(state.public)
	}
	if err != nil {
		return nil, err
	}
	return state, nil
}

// Normal writes do not migrate. Only an empty root may be initialized here;
// legacy/v1 input and an incomplete current pair require the startup workflow.
func loadState(root *os.Root, key string) (*storedState, error) {
	public, original, existed, err := readDocument(root, key, false)
	if err != nil {
		return nil, err
	}
	if !existed {
		if pending, err := unmigratedInputs(root); err != nil {
			return nil, err
		} else if pending {
			return nil, ErrNeedsMigration
		}
		return &storedState{public: &document{Version: Version}, raw: &document{Version: Version}, keys: keySnapshot{doc: emptyKeys()}}, nil
	}
	if public.Version != Version {
		return nil, ErrNeedsMigration
	}
	keys, err := readKeys(root)
	if err != nil {
		return nil, err
	}
	if !keys.exists {
		return nil, errors.New("key.json is required for current configuration")
	}
	raw, err := resolveDocument(public, keys.doc)
	if err != nil {
		return nil, err
	}
	return &storedState{public: public, raw: raw, original: original, existed: true, keys: keys}, nil
}

// Nonsecret reads never load key.json. Secret reads verify the main snapshot a
// second time: key-first publication and later pruning can overlap a reader.
func resolvedSection(root *os.Root, key string, doc *document, mainData []byte, section string) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		value := *doc.field(section)
		if doc.Version != Version {
			return nil, ErrNeedsMigration
		}
		if len(value) == 0 || section != Models && section != MCP && section != Search {
			return bytes.Clone(value), nil
		}
		only := &document{Version: Version}
		*only.field(section) = value
		refs, err := referencedKeys(only)
		if err != nil {
			return nil, err
		}
		if len(refs) == 0 {
			return bytes.Clone(value), nil
		}
		keys, keyErr := readKeys(root)
		var hydrated *document
		if keyErr == nil {
			hydrated, keyErr = resolveDocument(only, keys.doc)
		}
		next, nextData, present, readErr := readDocument(root, key, true)
		if readErr != nil {
			return nil, readErr
		}
		if !present {
			return nil, ErrConflict
		}
		if digest(mainData) != digest(nextData) {
			doc, mainData = next, nextData
			continue
		}
		if keyErr != nil {
			return nil, keyErr
		}
		return bytes.Clone(*hydrated.field(section)), nil
	}
	return nil, ErrConflict
}

func encodeKeys(keys *keyDocument) ([]byte, error) {
	data, err := marshalConfiguration(keys, true)
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, errors.New("key.json exceeds 1 MiB")
	}
	if _, err := decodeKeys(data); err != nil {
		return nil, err
	}
	return data, nil
}

func backupState(root *os.Root, state *storedState) error {
	if state.existed && state.public.Version == Version {
		return nil
	}
	var extra []backupFile
	if state.existed {
		extra = append(extra, backupFile{name: Filename, data: state.original, present: true})
	}
	if state.keys.exists {
		extra = append(extra, backupFile{name: KeyFilename, data: state.keys.data, present: true})
	}
	return prepareBackup(root, state.legacy, extra...)
}

// Existing referenced entries are immutable during publication. New entries
// are durable before the main document points at them; only old references
// dropped by this committed update are subsequently removed. Pre-existing
// unreferenced entries are deliberately not guessed to be disposable.
func completePairCommit(root *os.Root, key string, state *storedState) error {
	public, expanded, removed, err := separateDocument(state.raw, state.public, state.keys.doc)
	if err != nil {
		return err
	}
	keyData, err := encodeKeys(expanded)
	if err != nil {
		return err
	}
	if err := verifySnapshot(root, Filename, state.original, state.existed); err != nil {
		return err
	}
	if !state.keys.exists || !bytes.Equal(state.keys.data, keyData) {
		_, err := commitFile(root, KeyFilename, keyData, state.keys.data, state.keys.exists)
		if err != nil {
			return err
		}
	} else if err := verifySnapshot(root, KeyFilename, state.keys.data, true); err != nil {
		return err
	}
	if err := verifySnapshot(root, KeyFilename, keyData, true); err != nil {
		return err
	}
	committed, err := commit(root, public, state.original, state.existed, func() error {
		if err := verifyLegacy(root, state.legacy); err != nil {
			return err
		}
		return verifySnapshot(root, KeyFilename, keyData, true)
	})
	if !committed {
		return err
	}
	invalidate(key)
	if err != nil {
		log.Printf("configuration saved; directory durability unconfirmed, migration inputs and prior key entries retained: %v", err)
		return nil
	}
	if len(removed) != 0 {
		for _, ref := range removed {
			delete(expanded.Entries, ref)
		}
		pruned, cleanupErr := encodeKeys(expanded)
		if cleanupErr == nil {
			_, cleanupErr = commitFile(root, KeyFilename, pruned, keyData, true)
		}
		if cleanupErr != nil {
			log.Printf("configuration saved; previous static-key cleanup incomplete: %v", cleanupErr)
		}
	}
	if err := retireLegacy(root, state.legacy); err != nil {
		log.Printf("configuration saved; legacy cleanup incomplete: %v", err)
	}
	return nil
}
