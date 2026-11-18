package provider

import (
	"slices"
	"testing"
)

func TestSpecializedOutputsAreRetainedWithoutBecomingChat(t *testing.T) {
	draft := `{"openrouter":{"id":"openrouter","models":{
 "chat":{"id":"chat","name":"Text and image reply","modalities":{"input":["text"],"output":["text","image"]}},
 "judge":{"id":"judge","name":"Decision","type":"decision","modalities":{"input":["text","image"],"output":["text"]}},
 "movie":{"id":"movie","name":"Video","type":"video","modalities":{"input":["text","image"],"output":["video"]}}}}}`
	c, err := decodeModelsDevCatalog([]byte(draft))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers["openrouter"]) != 1 || !slices.Equal(c.Providers["openrouter"]["chat"].Output, []string{"text", "image"}) {
		t.Fatal("specialized models entered the conversation directory or outputs were lost")
	}
	judge, ok := c.Specialized["openrouter"]["judge"]
	if !ok || judge.Kind != "decision" || !slices.Equal(judge.Output, []string{"text"}) {
		t.Fatal("decision output metadata lost")
	}
	movie, ok := c.Specialized["openrouter"]["movie"]
	if !ok || movie.Kind != "video" || !slices.Contains(movie.Input, "image") {
		t.Fatal("video output metadata lost")
	}
	previous := c.Revision
	movie.Input = []string{"text"}
	c.Specialized["openrouter"]["movie"] = movie
	if next, err := c.revision(); err != nil || next == previous {
		t.Fatalf("media-only catalog change did not change the revision: %v", err)
	}
}

func TestLegacyTypedRowsAreSelectableWithoutAFullMediaSnapshot(t *testing.T) {
	snapshot := &OMPCatalogSnapshot{data: ompCatalogFile{ModelsDev: &modelsDevCatalog{Schema: 1, Types: map[string]map[string]string{
		"openai": {"uncategorized-spelling": "image", "image-in-name": "chat"},
	}}}}
	rows := snapshot.specializedModels("openai", "image")
	if len(rows) != 1 || rows[0].ID != "uncategorized-spelling" {
		t.Fatalf("lost explicit legacy classification: %+v", rows)
	}
}

func TestLegacyModelCatalogKeepsDeclaredMediaAPIs(t *testing.T) {
	for _, entry := range []struct{ id, kind, api string }{
		{"unfamiliar-id", "image", "openai-images"}, {"unfamiliar-id", "video", "openrouter-video"}, {"unfamiliar-id", "decision", "openrouter-decisions"},
		{"arbitrary-video-name", "", "openai-responses"},
	} {
		if got := OutputKind(ompOutput(entry.id, OMPModel{API: entry.api})); got != entry.kind {
			t.Fatalf("%s gave %q; want %q", entry.api, got, entry.kind)
		}
	}
	if len(SpecializedModels("openrouter", "video")) == 0 || len(SpecializedModels("openrouter", "decision")) == 0 {
		t.Fatal("embedded catalog lost its media and decision choices")
	}
}
