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
