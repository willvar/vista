package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const KeyFilename = "key.json"
const KeyVersion = 1

type credential struct {
	Section string                     `json:"section"`
	Owner   string                     `json:"owner"`
	Values  map[string]json.RawMessage `json:"values"`
}

type keyDocument struct {
	Version int                   `json:"version"`
	Entries map[string]credential `json:"entries"`
}

func emptyKeys() *keyDocument {
	return &keyDocument{Version: KeyVersion, Entries: map[string]credential{}}
}

// Objects retain unknown properties, including their spelling and raw values.
// Only fields interpreted by the codec participate in alias detection.
type credentialProperty struct {
	name  string
	value json.RawMessage
}

type credentialObject []credentialProperty

func decodeCredentialObject(data []byte) (credentialObject, error) {
	invalid := errors.New("invalid static credential object")
	if !utf8.Valid(data) {
		return nil, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	object := credentialObject{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, invalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, invalid
		}
		object = append(object, credentialProperty{name, value})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return nil, invalid
	}
	return object, nil
}

func (object credentialObject) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for index, property := range object {
		if index != 0 {
			out.WriteByte(',')
		}
		name, _ := marshalConfiguration(property.name, false)
		out.Write(name)
		out.WriteByte(':')
		if !utf8.Valid(property.value) || !json.Valid(property.value) {
			return nil, errors.New("invalid static credential value")
		}
		out.Write(property.value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// strictCredentialFields accepts exact schema keys and rejects duplicates.
func strictCredentialFields(object credentialObject, names ...string) (map[string]json.RawMessage, error) {
	fields := make(map[string]json.RawMessage, len(object))
	for _, property := range object {
		known := false
		for _, name := range names {
			known = known || property.name == name
		}
		if !known || fields[property.name] != nil {
			return nil, errors.New("invalid key.json fields")
		}
		fields[property.name] = property.value
	}
	if len(fields) != len(names) {
		return nil, errors.New("missing key.json fields")
	}
	return fields, nil
}

func credentialFields(section string) []string {
	switch section {
	case Models:
		return []string{"apiKey", "accessToken", "authToken", "appCode", "headers"}
	case MCP:
		return []string{"token", "clientSecret", "headers"}
	case Search:
		return []string{"apiKeys", "jinaApiKey", "deepseekFetchApiKey"}
	}
	return nil
}

func validateCredentialKeys(keys *keyDocument) error {
	if keys == nil || keys.Version != KeyVersion || keys.Entries == nil {
		return errors.New("invalid key.json document")
	}
	for ref, entry := range keys.Entries {
		fields := credentialFields(entry.Section)
		if ref == "" || fields == nil || entry.Owner == "" || entry.Values == nil {
			return errors.New("invalid key.json entry")
		}
		for name, value := range entry.Values {
			known := false
			for _, field := range fields {
				known = known || name == field
			}
			if !known || !utf8.Valid(value) || !json.Valid(value) {
				return errors.New("invalid key.json credential fields")
			}
		}
	}
	return nil
}

func decodeKeys(data []byte) (*keyDocument, error) {
	object, err := decodeCredentialObject(data)
	if err != nil {
		return nil, err
	}
	fields, err := strictCredentialFields(object, "version", "entries")
	if err != nil {
		return nil, err
	}
	keys := emptyKeys()
	if json.Unmarshal(fields["version"], &keys.Version) != nil || keys.Version != KeyVersion || bytes.Equal(bytes.TrimSpace(fields["version"]), []byte("null")) {
		return nil, errors.New("unsupported key.json version")
	}
	entries, err := decodeCredentialObject(fields["entries"])
	if err != nil {
		return nil, err
	}
	for _, property := range entries {
		if _, exists := keys.Entries[property.name]; exists {
			return nil, errors.New("duplicate key.json entry")
		}
		object, err := decodeCredentialObject(property.value)
		if err != nil {
			return nil, err
		}
		fields, err := strictCredentialFields(object, "section", "owner", "values")
		if err != nil {
			return nil, err
		}
		var entry credential
		if json.Unmarshal(fields["section"], &entry.Section) != nil || json.Unmarshal(fields["owner"], &entry.Owner) != nil {
			return nil, errors.New("invalid key.json entry identity")
		}
		values, err := decodeCredentialObject(fields["values"])
		if err != nil {
			return nil, err
		}
		entry.Values = make(map[string]json.RawMessage, len(values))
		for _, value := range values {
			if _, exists := entry.Values[value.name]; exists {
				return nil, errors.New("duplicate key.json credential field")
			}
			entry.Values[value.name] = bytes.Clone(value.value)
		}
		keys.Entries[property.name] = entry
	}
	if err := validateCredentialKeys(keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func cloneCredentialKeys(keys *keyDocument) (*keyDocument, error) {
	if keys == nil {
		return emptyKeys(), nil
	}
	if err := validateCredentialKeys(keys); err != nil {
		return nil, err
	}
	cloned := emptyKeys()
	for ref, entry := range keys.Entries {
		values := make(map[string]json.RawMessage, len(entry.Values))
		for name, value := range entry.Values {
			values[name] = bytes.Clone(value)
		}
		entry.Values = values
		cloned.Entries[ref] = entry
	}
	return cloned, nil
}

// credentialIndexes matches encoding/json's case-insensitive known fields but
// rejects aliases of the same field instead of depending on object order.
func credentialIndexes(object credentialObject, names ...string) (map[string]int, error) {
	indexes := map[string]int{}
	for index, property := range object {
		for _, name := range names {
			if !strings.EqualFold(property.name, name) {
				continue
			}
			if _, exists := indexes[name]; exists {
				return nil, errors.New("ambiguous static credential fields")
			}
			indexes[name] = index
			break
		}
	}
	return indexes, nil
}

type credentialLocation struct {
	owner  string
	ref    string
	values map[string]json.RawMessage
}

func inspectCredentialObject(section string, object credentialObject) (credentialLocation, error) {
	fields := credentialFields(section)
	names := append([]string{"credentialRef"}, fields...)
	switch section {
	case Models:
		names = append(names, "connectionId", "key")
	case MCP:
		names = append(names, "id")
	}
	indexes, err := credentialIndexes(object, names...)
	if err != nil {
		return credentialLocation{}, err
	}
	location := credentialLocation{values: map[string]json.RawMessage{}}
	for _, name := range fields {
		if index, present := indexes[name]; present {
			location.values[name] = bytes.Clone(object[index].value)
		}
	}
	if index, present := indexes["credentialRef"]; present {
		value := bytes.TrimSpace(object[index].value)
		if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &location.ref) != nil || location.ref == "" {
			return credentialLocation{}, errors.New("invalid static credential reference")
		}
	}
	if len(location.values) == 0 && location.ref == "" {
		return location, nil
	}
	identity := func(name string) (string, error) {
		var value string
		if index, present := indexes[name]; present && json.Unmarshal(object[index].value, &value) != nil {
			return "", errors.New("invalid static credential owner")
		}
		return value, nil
	}
	switch section {
	case Models:
		value, identityErr := identity("connectionId")
		err = identityErr
		if err == nil && value != "" {
			location.owner = "connection:" + value
		} else if err == nil {
			value, err = identity("key")
			if value != "" {
				location.owner = "key:" + value
			}
		}
	case MCP:
		value, identityErr := identity("id")
		err = identityErr
		if value != "" {
			location.owner = "id:" + value
		}
	case Search:
		location.owner = Search
	}
	if err != nil || location.owner == "" {
		return credentialLocation{}, errors.New("static credentials require a stable owner")
	}
	return location, nil
}

// transformCredentialDocument only visits the three managed public locations.
// Other raw sections, nulls, and missing values are cloned without expansion.
func transformCredentialDocument(source *document, change func(string, credentialObject) (credentialObject, error)) (*document, error) {
	if source == nil {
		return nil, errors.New("missing static credential configuration")
	}
	cloned := *source
	for _, binding := range bindings {
		*cloned.field(binding.section) = bytes.Clone(*source.field(binding.section))
	}
	absent := func(data json.RawMessage) bool {
		return len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null"))
	}
	transformObject := func(section string, data json.RawMessage) (json.RawMessage, error) {
		if absent(data) {
			return data, nil
		}
		object, err := decodeCredentialObject(data)
		if err != nil {
			return nil, err
		}
		object, err = change(section, object)
		if err != nil {
			return nil, err
		}
		return marshalConfiguration(object, false)
	}
	transformArray := func(section string, data json.RawMessage) (json.RawMessage, error) {
		if absent(data) {
			return data, nil
		}
		var items []json.RawMessage
		if !utf8.Valid(data) || json.Unmarshal(data, &items) != nil {
			return nil, errors.New("invalid static credential collection")
		}
		for index, item := range items {
			var err error
			items[index], err = transformObject(section, item)
			if err != nil {
				return nil, err
			}
		}
		return marshalConfiguration(items, false)
	}
	if !absent(cloned.Models) {
		object, err := decodeCredentialObject(cloned.Models)
		if err != nil {
			return nil, err
		}
		indexes, err := credentialIndexes(object, "models")
		if err != nil {
			return nil, err
		}
		if index, present := indexes["models"]; present {
			object[index].value, err = transformArray(Models, object[index].value)
			if err != nil {
				return nil, err
			}
		}
		cloned.Models, err = marshalConfiguration(object, false)
		if err != nil {
			return nil, err
		}
	}
	var err error
	if cloned.MCP, err = transformArray(MCP, cloned.MCP); err != nil {
		return nil, err
	}
	if cloned.Search, err = transformObject(Search, cloned.Search); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func inspectPublicCredential(section string, object credentialObject) (credentialLocation, error) {
	location, err := inspectCredentialObject(section, object)
	if err != nil {
		return credentialLocation{}, err
	}
	if len(location.values) != 0 {
		return credentialLocation{}, errors.New("inline static credentials are not allowed in config.json")
	}
	return location, nil
}

// Root version validation belongs to the main-document decoder. These helpers
// validate the credential boundary without changing typed section semantics.
func validatePublicDocument(public *document) error {
	_, err := referencedKeys(public)
	return err
}

func referencedKeys(public *document) (map[string]bool, error) {
	refs := map[string]bool{}
	_, err := transformCredentialDocument(public, func(section string, object credentialObject) (credentialObject, error) {
		location, err := inspectPublicCredential(section, object)
		if err != nil {
			return nil, err
		}
		if location.ref != "" {
			refs[location.ref] = true
		}
		return object, nil
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func lookupCredential(keys *keyDocument, section string, location credentialLocation) (credential, error) {
	entry, present := keys.Entries[location.ref]
	if !present || entry.Section != section || entry.Owner != location.owner {
		return credential{}, errors.New("static credential reference is missing or does not match its owner")
	}
	return entry, nil
}

func resolveDocument(public *document, keys *keyDocument) (*document, error) {
	if keys == nil {
		keys = emptyKeys()
	}
	if err := validateCredentialKeys(keys); err != nil {
		return nil, err
	}
	return transformCredentialDocument(public, func(section string, object credentialObject) (credentialObject, error) {
		location, err := inspectPublicCredential(section, object)
		if err != nil || location.ref == "" {
			return object, err
		}
		entry, err := lookupCredential(keys, section, location)
		if err != nil {
			return nil, err
		}
		resolved := make(credentialObject, 0, len(object)+len(entry.Values))
		for _, property := range object {
			if !strings.EqualFold(property.name, "credentialRef") {
				resolved = append(resolved, property)
			}
		}
		for _, name := range credentialFields(section) {
			if value, present := entry.Values[name]; present {
				resolved = append(resolved, credentialProperty{name, bytes.Clone(value)})
			}
		}
		return resolved, nil
	})
}

// UseNumber avoids rounding large JSON numbers while stable marshaling makes
// object ordering and insignificant whitespace irrelevant to bundle equality.
func credentialBundle(values map[string]json.RawMessage) (string, error) {
	data, err := json.Marshal(values)
	if err != nil {
		return "", errors.New("invalid static credential bundle")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", errors.New("invalid static credential bundle")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("invalid static credential bundle")
	}
	return string(canonical), nil
}

type credentialMatch struct {
	section, owner, bundle string
}

// separateDocument appends immutable entries, never reactivating an orphan.
// The caller writes expanded keys, publishes public, then removes removedRefs.
func separateDocument(raw *document, previous *document, keys *keyDocument) (public *document, expanded *keyDocument, removedRefs []string, err error) {
	expanded, err = cloneCredentialKeys(keys)
	if err != nil {
		return nil, nil, nil, err
	}
	active := map[credentialMatch]string{}
	previousRefs := map[string]bool{}
	if previous != nil && previous.Version == 2 {
		_, err = transformCredentialDocument(previous, func(section string, object credentialObject) (credentialObject, error) {
			location, err := inspectPublicCredential(section, object)
			if err != nil || location.ref == "" {
				return object, err
			}
			entry, err := lookupCredential(expanded, section, location)
			if err != nil {
				return nil, err
			}
			bundle, err := credentialBundle(entry.Values)
			if err != nil {
				return nil, err
			}
			match := credentialMatch{section, location.owner, bundle}
			if _, present := active[match]; !present {
				active[match] = location.ref
			}
			previousRefs[location.ref] = true
			return object, nil
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	newRefs := map[string]bool{}
	created := map[credentialMatch]string{}
	public, err = transformCredentialDocument(raw, func(section string, object credentialObject) (credentialObject, error) {
		location, err := inspectCredentialObject(section, object)
		if err != nil {
			return nil, err
		}
		if location.ref != "" {
			return nil, errors.New("logical configuration must not contain static credential references")
		}
		if len(location.values) == 0 {
			return object, nil
		}
		bundle, err := credentialBundle(location.values)
		if err != nil {
			return nil, err
		}
		match := credentialMatch{section, location.owner, bundle}
		ref := active[match]
		if ref == "" {
			ref = created[match]
		}
		if ref == "" {
			for {
				ref = "cred-" + uuid.NewString()
				if _, exists := expanded.Entries[ref]; !exists {
					break
				}
			}
			expanded.Entries[ref] = credential{Section: section, Owner: location.owner, Values: location.values}
			created[match] = ref
		}
		newRefs[ref] = true
		separated := make(credentialObject, 0, len(object)+1)
		for _, property := range object {
			secret := false
			for _, name := range credentialFields(section) {
				secret = secret || strings.EqualFold(property.name, name)
			}
			if !secret {
				separated = append(separated, property)
			}
		}
		encodedRef, _ := marshalConfiguration(ref, false)
		separated = append(separated, credentialProperty{"credentialRef", encodedRef})
		return separated, nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	public.Version = Version
	for ref := range previousRefs {
		if !newRefs[ref] {
			removedRefs = append(removedRefs, ref)
		}
	}
	sort.Strings(removedRefs)
	return public, expanded, removedRefs, nil
}
