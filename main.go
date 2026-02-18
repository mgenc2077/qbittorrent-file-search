package main

import (
	"database/sql"
	"encoding/json"
	"net/http"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

type MainData struct {
	Torrents TorrentsList `json:"torrents"`
}

type TorrentsList []string

type File struct {
	Name string `json:"name"`
}

type FilesList []File

func (t *TorrentsList) UnmarshalJSON(b []byte) error {
	var temp map[string]json.RawMessage
	if err := json.Unmarshal(b, &temp); err != nil {
		return err
	}
	for k := range temp {
		*t = append(*t, k)
	}
	return nil
}

func main() {
	// create sqlite db
	db, err := sql.Open("sqlite3", "./test.db")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// create table
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS files (hash TEXT, name TEXT)")
	if err != nil {
		panic(err)
	}

	resp, err := http.Get("http://localhost:21444/api/v2/sync/maindata?rid=67")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var mainData MainData
	err = json.NewDecoder(resp.Body).Decode(&mainData)
	if err != nil {
		panic(err)
	}

	for _, hash := range mainData.Torrents {
		files, err := http.Get("http://localhost:21444/api/v2/torrents/files?hash=" + hash)
		if err != nil {
			panic(err)
		}
		defer files.Body.Close()
		var filesData FilesList
		err = json.NewDecoder(files.Body).Decode(&filesData)
		if err != nil {
			panic(err)
		}
		for _, file := range filesData {
			res, err := db.Exec("INSERT INTO files (hash, name) VALUES (?, ?)", hash, file.Name)
			if err != nil {
				panic(err)
			}
			_ = res
		}
	}
}
