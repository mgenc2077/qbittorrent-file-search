package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "embed"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
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

type task struct {
	taskname string
	task     interface{}
}

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

	godotenv.Load()

	pg_dsn := os.Getenv("PG_DSN")

	var db *sql.DB
	var err error

	if pg_dsn != "" {
		// open postgres db
		db, err = sql.Open("postgres", pg_dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
	} else {
		db, err = openSQLite("index.db")
		if err != nil {
			log.Fatal(err)
		}

	}

	// create table
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS files (hash TEXT, name TEXT, \"index\" INT, progress FLOAT, status TEXT DEFAULT 'cached')")
	if err != nil {
		panic(err)
	}

	// create unique index for UPSERT logic
	_, err = db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_files_hash_name ON files(hash, name)")
	if err != nil {
		panic(err)
	}

	log.Println("Building Database...")
	getFiles(db)
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
		rows, err := db.Query("SELECT hash, name, \"index\", progress, status FROM files WHERE name LIKE $1", "%"+query+"%")
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
		rows := db.QueryRow("SELECT hash, name, \"index\", progress FROM files WHERE name = $1", name)
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

	// Capture cookies from the initial response to use in subsequent requests
	cookies := resp.Cookies()

	ctx := context.Background()
	results := make(chan FilesList, 10_000)

	// Writer runs in exactly one goroutine taking all files to bulk insert
	writerErr := make(chan error, 1)
	go func() {
		writerErr <- writerLoop(ctx, db, results, WriterOptions{
			BatchSize:     5000,
			FlushInterval: 100 * time.Millisecond,
		})
	}()

	g, _ := errgroup.WithContext(ctx)
	// Limiting concurrency can be helpful to prevent file descriptor exhaustion
	g.SetLimit(5)

	for _, hash := range mainData.Torrents {
		hash := hash // capture locally
		g.Go(func() error {
			client := &http.Client{
				Timeout: 10 * time.Second,
			}

			req, err := http.NewRequest("GET", "http://localhost:21444/api/v2/torrents/files", nil)
			if err != nil {
				return err
			}
			// Set the cookies captured from the initial request
			for _, c := range cookies {
				req.AddCookie(c)
			}
			req.Header.Set("Referer", "http://localhost:21444/")
			q := req.URL.Query()
			q.Add("hash", hash)
			req.URL.RawQuery = q.Encode()

			files, err := client.Do(req)
			if err != nil {
				return err
			}
			defer files.Body.Close()

			var filesData FilesList
			if err := json.NewDecoder(files.Body).Decode(&filesData); err != nil {
				return err
			}

			// Add the torrent hash to each file
			for i := range filesData {
				filesData[i].Hash = hash
			}

			// Send the batch of formatted files to writer
			results <- filesData
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatalf("failed to process torrents: %v", err)
	}

	// Close results channel to signal the writer that no more data is coming
	close(results)

	// Wait for the writer to finish
	if err := <-writerErr; err != nil {
		log.Fatalf("writer loop error: %v", err)
	}
}

func openSQLite(path string) (*sql.DB, error) {
	// If your driver prefers a file: URL, keep this.
	// If plain paths work for you, you can just use path directly.
	dsn := "file:" + path

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	// Critical for SQLite correctness/perf with database/sql:
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// Pragmas for fast writes:
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		return nil, err
	}

	return db, nil
}

type WriterOptions struct {
	BatchSize     int           // e.g. 1000..10000
	FlushInterval time.Duration // e.g. 50ms..500ms
}

func writerLoop(ctx context.Context, db *sql.DB, in <-chan FilesList, opt WriterOptions) error {
	if opt.BatchSize <= 0 {
		opt.BatchSize = 1000
	}
	if opt.FlushInterval <= 0 {
		opt.FlushInterval = 100 * time.Millisecond
	}

	ticker := time.NewTicker(opt.FlushInterval)
	defer ticker.Stop()

	batch := make([]File, 0, opt.BatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		tx, err := db.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// Prepare once per tx (fast)
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO files (hash, name, "index", progress) VALUES ($1, $2, $3, $4) ON CONFLICT (hash, name) DO UPDATE SET status = 'cached'`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, r := range batch {
			if _, err := stmt.ExecContext(ctx, r.Hash, r.Name, r.Index, r.Progress); err != nil {
				return err
			}
		}

		if err := tx.Commit(); err != nil {
			return err
		}

		// reset without realloc
		batch = batch[:0]
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			// best-effort final flush on cancellation
			_ = flush()
			return ctx.Err()

		case r, ok := <-in:
			if !ok {
				// channel closed => flush remaining and exit
				return flush()
			}
			batch = append(batch, r...)
			if len(batch) >= opt.BatchSize {
				if err := flush(); err != nil {
					return err
				}
			}

		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}
