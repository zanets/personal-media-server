package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	_ "modernc.org/sqlite"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed dist/*
var assets embed.FS

type Note struct {
	Time float64 `json:"time"`
	Text string  `json:"text"`
}
type Media struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Size     int64    `json:"size"`
	Rating   int      `json:"rating"`
	Tags     []string `json:"tags"`
	Notes    []Note   `json:"notes"`
	Position float64  `json:"position"`
	Kind     string   `json:"kind"`
}
type App struct {
	db     *sql.DB
	roots  []string
	scanMu sync.Mutex
}
type folders []string

func (f *folders) String() string     { return strings.Join(*f, ", ") }
func (f *folders) Set(v string) error { *f = append(*f, v); return nil }

var extensions = map[string]string{".mp4": "video", ".webm": "video", ".m4v": "video", ".mov": "video", ".mkv": "video", ".avi": "video", ".mp3": "audio", ".m4a": "audio", ".wav": "audio", ".ogg": "audio", ".flac": "audio"}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS media(id INTEGER PRIMARY KEY,path TEXT UNIQUE,title TEXT,size INTEGER,rating INTEGER DEFAULT 0,tags TEXT DEFAULT '[]',notes TEXT DEFAULT '[]',position REAL DEFAULT 0)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (a *App) allowed(path string) bool {
	for _, root := range a.roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
func (a *App) scan() (int, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for _, root := range a.roots {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if path != root && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if extensions[strings.ToLower(filepath.Ext(path))] == "" {
				return nil
			}
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			if !a.allowed(real) {
				return nil
			}
			info, err := os.Stat(real)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			_, err = tx.Exec(`INSERT INTO media(path,title,size) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET size=excluded.size`, real, strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), info.Size())
			if err == nil {
				count++
			}
			return err
		})
		if err != nil {
			return 0, err
		}
	}
	return count, tx.Commit()
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
func failure(w http.ResponseWriter, status int, s string) {
	reply(w, status, map[string]string{"error": s})
}
func (a *App) list(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT id,title,size,rating,tags,notes,position,path FROM media ORDER BY id DESC`)
	if err != nil {
		failure(w, 500, "讀取媒體庫失敗")
		return
	}
	defer rows.Close()
	items := []Media{}
	for rows.Next() {
		var m Media
		var tags, notes, path string
		if err = rows.Scan(&m.ID, &m.Title, &m.Size, &m.Rating, &tags, &notes, &m.Position, &path); err != nil {
			failure(w, 500, "讀取資料失敗")
			return
		}
		if json.Unmarshal([]byte(tags), &m.Tags) != nil || json.Unmarshal([]byte(notes), &m.Notes) != nil {
			failure(w, 500, "資料格式錯誤")
			return
		}
		m.Kind = extensions[strings.ToLower(filepath.Ext(path))]
		items = append(items, m)
	}
	if rows.Err() != nil {
		failure(w, 500, "讀取資料失敗")
		return
	}
	reply(w, 200, items)
}
func (a *App) stream(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		failure(w, 404, "找不到影音")
		return
	}
	var path string
	if err = a.db.QueryRow("SELECT path FROM media WHERE id=?", id).Scan(&path); err != nil {
		failure(w, 404, "找不到影音")
		return
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		failure(w, 404, "檔案已移動或無法讀取")
		return
	}
	if !a.allowed(real) {
		failure(w, 403, "檔案不在已授權資料夾內")
		return
	}
	f, err := os.Open(real)
	if err != nil {
		failure(w, 404, "無法開啟影音")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		failure(w, 404, "無法讀取影音")
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

type Update struct {
	ID       int64     `json:"id"`
	Rating   *int      `json:"rating"`
	Tags     *[]string `json:"tags"`
	Notes    *[]Note   `json:"notes"`
	Position *float64  `json:"position"`
}

func (a *App) update(w http.ResponseWriter, r *http.Request) {
	var u Update
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		failure(w, 400, "資料格式不正確")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		failure(w, 400, "資料格式不正確")
		return
	}
	fields := []string{}
	args := []any{}
	add := func(k string, v any) { fields = append(fields, k+"=?"); args = append(args, v) }
	if u.Rating != nil {
		if *u.Rating < 0 || *u.Rating > 5 {
			failure(w, 400, "星等須為 0 到 5")
			return
		}
		add("rating", *u.Rating)
	}
	if u.Position != nil {
		if *u.Position < 0 || *u.Position > 1e9 {
			failure(w, 400, "時間格式錯誤")
			return
		}
		add("position", *u.Position)
	}
	if u.Tags != nil {
		if len(*u.Tags) > 100 {
			failure(w, 400, "標籤數量過多")
			return
		}
		tags := []string{}
		seen := map[string]bool{}
		for _, t := range *u.Tags {
			t = strings.TrimSpace(t)
			if len(t) > 400 {
				failure(w, 400, "標籤太長")
				return
			}
			if t != "" && !seen[t] {
				tags = append(tags, t)
				seen[t] = true
			}
		}
		b, _ := json.Marshal(tags)
		add("tags", string(b))
	}
	if u.Notes != nil {
		if len(*u.Notes) > 1000 {
			failure(w, 400, "筆記數量過多")
			return
		}
		for _, n := range *u.Notes {
			if n.Time < 0 || n.Time > 1e9 || strings.TrimSpace(n.Text) == "" || len(n.Text) > 40000 {
				failure(w, 400, "筆記格式錯誤")
				return
			}
		}
		b, _ := json.Marshal(*u.Notes)
		add("notes", string(b))
	}
	if len(fields) == 0 {
		failure(w, 400, "沒有可更新的欄位")
		return
	}
	args = append(args, u.ID)
	res, err := a.db.Exec("UPDATE media SET "+strings.Join(fields, ",")+" WHERE id=?", args...)
	if err != nil {
		failure(w, 500, "儲存失敗")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		failure(w, 404, "找不到影音")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/media", a.list)
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"folders": a.roots}) })
	mux.HandleFunc("POST /api/update", a.update)
	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		count, err := a.scan()
		if err != nil {
			log.Printf("scan: %v", err)
			failure(w, 500, "掃描失敗，請確認資料夾權限")
			return
		}
		reply(w, 200, map[string]int{"count": count})
	})
	mux.HandleFunc("GET /stream/{id}", a.stream)
	public, _ := fs.Sub(assets, "dist")
	static := http.FileServer(http.FS(public))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" && r.URL.Path != "/vendor/react.production.min.js" && r.URL.Path != "/vendor/react-dom.production.min.js" {
			http.NotFound(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					failure(w, 403, "拒絕跨站操作")
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				failure(w, 403, "拒絕跨站操作")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func main() {
	var roots folders
	flag.Var(&roots, "media", "影音資料夾，可重複指定")
	host := flag.String("host", "0.0.0.0", "監聽位址")
	port := flag.Int("port", 8765, "連接埠")
	dbpath := flag.String("db", "library.sqlite3", "資料庫路徑")
	flag.Parse()
	if len(roots) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	for i, p := range roots {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			log.Fatal(err)
		}
		real, err = filepath.Abs(real)
		if err != nil {
			log.Fatal(err)
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			log.Fatalf("不是資料夾：%s", p)
		}
		roots[i] = real
	}
	db, err := openDB(*dbpath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db, roots: roots}
	count, err := a.scan()
	if err != nil {
		log.Fatal(err)
	}
	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("映格 FRAME｜已索引 %d 個影音\n本機：http://localhost:%d\n手機／筆電：使用本機區網 IP，連接埠 %d\n", count, *port, *port)
	server := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
