package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	listenAddr   = flag.String("listen", "localhost:11106", "address to listen on")
	endpointAddr = flag.String("endpoint", "https://example.com/abcs", "endpoint to send messages to")
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("error: %v", err)
	}
}

func sendMessage(to, msg string) error {
	path, err := os.Getwd()
	if err != nil {
		return err
	}
	cmd := exec.Command("./send_message.sh", to, msg)
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return err
	}
	log.Printf("(to %s) %s", to, msg)
	return nil
}

func sendMessageHandler() http.HandlerFunc {
	type request struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := sendMessage(req.To, req.Message); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// to-do: check if message was delivered
		w.WriteHeader(http.StatusOK)
	}
}

func fromAppleTimestamp(ts int64) time.Time {
	r := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	return r.Add(time.Duration(ts) / 1000000000 * time.Second)
}

func toAppleTimestamp(t time.Time) int64 {
	r := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	return int64(t.Sub(r).Seconds()) * 1000000000
}

const (
	pollp   = time.Second
	meCheck = time.Second
	mq      = `SELECT rowid, text, handle_id, date FROM message WHERE date >= ? ORDER BY date desc`
	isme    = `SELECT is_from_me FROM message where rowid = ?;`
)

type message struct {
	content sql.NullString
	handle  int
	date    time.Time
}

func pollForUpdates(db *sql.DB, endpoint string) error {
	start := time.Now()
	for {
		time.Sleep(pollp)
		rows, err := db.Query(mq, toAppleTimestamp(start))
		if err != nil {
			return err
		}
		for rows.Next() {
			var mid int
			var m message
			var ts int64
			if err := rows.Scan(&mid, &m.content, &m.handle, &ts); err != nil {
				return err
			}
			m.date = fromAppleTimestamp(ts)
			// seems to be missing messages, and `isme` query not working
			// this is really dumb and only reason it is needed is that `is_from_me`
			// isn't updated immediately when the message row is inserted. it's fucking bs.
			// to-do: figure out a better way of doing this
			go func(id int, msg message) {
				log.Printf("here (rowid=%d)", id)
				time.Sleep(meCheck)
				var isMe int
				row := db.QueryRow(isme, id)
				if err := row.Scan(&isMe); err != nil {
					log.Fatalf("error checking for message ownership: %v", err)
				}
				if isMe == 0 {
					log.Printf("(from %d) %s (%s)", m.handle, m.content.String, m.date.String())
				}
			}(mid, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		start = time.Now()
	}
}

func run() error {
	flag.Parse()

	r := http.NewServeMux()
	r.HandleFunc("/", sendMessageHandler())

	s := &http.Server{
		Addr:         *listenAddr,
		Handler:      r,
		ReadTimeout:  time.Second,
		WriteTimeout: 10 * time.Second,
	}

	u, err := user.Current()
	if err != nil {
		return err
	}
	dbpath := fmt.Sprintf("/Users/%s/Library/Messages/chat.db", u.Username)

	log.Printf("opening sqlite3 db at %s", dbpath)
	db, err := sql.Open("sqlite3", dbpath)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		return err
	}
	log.Println("opened db connection")

	go func() {
		if err := pollForUpdates(db, *endpointAddr); err != nil {
			log.Fatalf("failed to poll for updates: %v", err)
		}
	}()

	log.Printf("starting web server on %s", *listenAddr)
	return s.ListenAndServe()
}
