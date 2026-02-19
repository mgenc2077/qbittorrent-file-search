package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	_ "embed"

	_ "github.com/lib/pq"
	"golang.org/x/sync/errgroup"
)

//go:embed index.html
var indexHTML []byte

type MainData struct {
	Torrents TorrentsList `json:"torrents"`
}

type TorrentsList []string

type File struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Index    int     `json:"index"`
	Progress float64 `json:"progress"`
	Status   string  `json:"status"`
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
	// open postgres db
	db, err := sql.Open("postgres", "postgres://admin:example@localhost:5433/qbittorrent-file-search?sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// create table
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS files (hash TEXT, name TEXT, index INT, progress FLOAT, status TEXT DEFAULT 'cached')")
	if err != nil {
		panic(err)
	}

	log.Println("Building Database...")
	//getFiles(db)
	log.Println("Database Ready")

	http.HandleFunc("/search", searchEndpoint(db))
	http.HandleFunc("/enable", enableDownload(db))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(indexHTML)
	})

	log.Println("Server started on :1182")
	http.ListenAndServe(":1182", nil)
}

func searchEndpoint(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}
		rows, err := db.Query("SELECT hash, name, index, progress, status FROM files WHERE name LIKE $1", "%"+query+"%")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var files []File
		for rows.Next() {
			var file File
			if err := rows.Scan(&file.Hash, &file.Name, &file.Index, &file.Progress, &file.Status); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			files = append(files, file)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(files)
	}
}

func enableDownload(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		rows := db.QueryRow("SELECT hash, name, index, progress FROM files WHERE name = $1", name)
		var file File
		rows.Scan(&file.Hash, &file.Name, &file.Index, &file.Progress)
		body := fmt.Sprintf("hash=%s&id=%d&priority=1", file.Hash, file.Index)
		http.Post("http://localhost:21444/api/v2/torrents/filePrio", "application/x-www-form-urlencoded", strings.NewReader(body))
		db.Query("UPDATE files SET status = 'downloading' WHERE name = $1", name)
		w.WriteHeader(http.StatusOK)
	}
}

func getFiles(db *sql.DB) {
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
		ctx := context.Background()
		g, _ := errgroup.WithContext(ctx)
		//g.SetLimit(10)
		g.Go(func() error {
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
			ctx2 := context.Background()
			k, _ := errgroup.WithContext(ctx2)
			for _, file := range filesData {
				k.Go(func() error {
					// Check if file already exists
					var exists bool
					err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM files WHERE hash = $1 AND name = $2)", hash, file.Name).Scan(&exists)
					if err != nil {
						return err
					}
					if exists {
						// update status if exists
						res, err := db.Exec("UPDATE files SET status = $1 WHERE hash = $2 AND name = $3", "cached", hash, file.Name)
						if err != nil {
							return err
						}
						_ = res
						return nil
					}
					res, err := db.Exec("INSERT INTO files (hash, name, index, progress) VALUES ($1, $2, $3, $4)", hash, file.Name, file.Index, file.Progress)
					if err != nil {
						return err
					}
					_ = res
					return nil
				})
				if err := k.Wait(); err != nil {
					log.Fatalf("failed to wait for group: %v", err)
				}
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			log.Fatalf("failed to wait for group: %v", err)
		}
	}
}
