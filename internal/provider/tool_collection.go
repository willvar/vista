package provider

import "errors"

// toolCollectionBytes bounds retained tool data rather than the number of calls.
// Each entry includes its key, payload strings and 64 bytes for item metadata;
// even empty streamed fragments therefore cannot create an unbounded collection.
// Updating an existing key replaces its size without counting replayed data twice.
type toolCollectionBytes struct {
	total int
	sizes map[string]int
}

func (b *toolCollectionBytes) set(key string, payloadBytes int) error {
	if b.sizes == nil {
		b.sizes = make(map[string]int)
	}
	size := len(key) + payloadBytes + 64
	if size > maxReplyBytes || b.total-b.sizes[key] > maxReplyBytes-size {
		return errors.New("model tool data exceeds 256 KiB")
	}
	b.total += size - b.sizes[key]
	b.sizes[key] = size
	return nil
}
