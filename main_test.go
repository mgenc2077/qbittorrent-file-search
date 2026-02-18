package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMainData(t *testing.T) {
	b, err := os.ReadFile("maindata-resp.json")
	if err != nil {
		t.Fatal(err)
	}
	var mainData MainData
	if err := json.Unmarshal(b, &mainData); err != nil {
		t.Fatal(err)
	}
	if len(mainData.Torrents) == 0 {
		t.Error("Expected torrents, got 0")
	}
	t.Logf("Found %d torrents", len(mainData.Torrents))
	// Verify some known hashes from the json file exist?
	// "0065eb2b7253b93caf00528508e28264d61160ef"
	found := false
	for _, hash := range mainData.Torrents {
		if hash == "0065eb2b7253b93caf00528508e28264d61160ef" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected hash 0065eb2b7253b93caf00528508e28264d61160ef not found")
	}
}
