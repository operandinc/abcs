package imessage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDuration is the minimum interval that must pass before opening the database again.
var DefaultDuration = 200 * time.Millisecond

// Incoming is represents a message from someone. This struct is filled out
// and sent to incoming callback methods and/or to bound channels.
type Incoming struct {
	RowID          int64  // RowID is the unique database row id.
	From           string // From is the handle of the user who sent the message.
	Text           string // Text is the body of the message.
	Attachment     []byte // Attachment data.
	AttachmentType string // Attachment MIME type.
}

// Callback is the type used to return an incoming message to the consuming app.
// Create a function that matches this interface to process incoming messages
// using a callback (as opposed to a channel).
type Callback func(msg Incoming)

type chanBinding struct {
	Match string
	Chan  chan Incoming
}

type funcBinding struct {
	Match string
	Func  Callback
}

type binds struct {
	Funcs []*funcBinding
	Chans []*chanBinding
	// locks either or both slices
	sync.RWMutex
}

// IncomingChan connects a channel to a matched string in a message.
// Similar to the IncomingCall method, this will send an incoming message
// to a channel. Any message with text matching `match` is sent. Regexp supported.
// Use '.*' for all messages. The channel blocks, so avoid long operations.
func (m *Messages) IncomingChan(match string, channel chan Incoming) {
	m.binds.Lock()
	defer m.binds.Unlock()
	m.Chans = append(m.Chans, &chanBinding{Match: match, Chan: channel})
}

// IncomingCall connects a callback function to a matched string in a message.
// This methods creates a callback that is run in a go routine any time
// a message containing `match` is found. Use '.*' for all messages. Supports regexp.
func (m *Messages) IncomingCall(match string, callback Callback) {
	m.binds.Lock()
	defer m.binds.Unlock()
	m.Funcs = append(m.Funcs, &funcBinding{Match: match, Func: callback})
}

// RemoveChan deletes a message match to channel made with IncomingChan()
func (m *Messages) RemoveChan(match string) int {
	m.binds.Lock()
	defer m.binds.Unlock()
	removed := 0
	for i, rlen := 0, len(m.Chans); i < rlen; i++ {
		j := i - removed
		if m.Chans[j].Match == match {
			m.Chans = append(m.Chans[:j], m.Chans[j+1:]...)
			removed++
		}
	}
	return removed
}

// RemoveCall deletes a message match to function callback made with IncomingCall()
func (m *Messages) RemoveCall(match string) int {
	m.binds.Lock()
	defer m.binds.Unlock()
	removed := 0
	for i, rlen := 0, len(m.Funcs); i < rlen; i++ {
		j := i - removed
		if m.Funcs[j].Match == match {
			m.Funcs = append(m.Funcs[:j], m.Funcs[j+1:]...)
			removed++
		}
	}
	return removed
}

// processIncomingMessages starts the iMessage-sqlite3 db watcher routine(s).
func (m *Messages) processIncomingMessages() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	go func() {
		m.fsnotifySQL(watcher, time.NewTicker(DefaultDuration))
		_ = watcher.Close()
	}()
	return watcher.Add(filepath.Dir(m.SQLPath))
}

func (m *Messages) fsnotifySQL(watcher *fsnotify.Watcher, ticker *time.Ticker) {
	for checkDB := false; ; {
		select {
		case msg, ok := <-m.inChan:
			if !ok {
				return
			}
			m.handleIncoming(msg)

		case <-ticker.C:
			if checkDB {
				m.checkForNewMessages()
			}

		case event, ok := <-watcher.Events:
			if !ok {
				m.ErrorLog.Print("fsnotify watcher failed. message routines stopped")
				m.Stop()
				return
			}
			checkDB = event.Op&fsnotify.Write == fsnotify.Write

		case err, ok := <-watcher.Errors:
			if !ok {
				m.ErrorLog.Print("fsnotify watcher errors failed. message routines stopped.")
				m.Stop()
				return
			}
			m.checkErr(err, "fsnotify watcher")
		}
	}
}

func (m *Messages) checkForNewMessages() {
	db, err := m.getDB()
	if err != nil || db == nil {
		return // error
	}
	defer m.closeDB(db)
	sql := `SELECT m.rowid as rowid, handle.id as handle, m.text as text, ` +
		`CASE cache_has_attachments ` +
		`WHEN 0 THEN Null ` +
		`WHEN 1 THEN filename ` +
		`END AS attachment, ` +
		`CASE cache_has_attachments ` +
		`WHEN 0 THEN Null ` +
		`WHEN 1 THEN mime_type ` +
		`END as attachment_type ` +
		`FROM message AS m ` +
		`LEFT JOIN message_attachment_join AS maj ON message_id = m.rowid ` +
		`LEFT JOIN attachment AS a ON a.rowid = maj.attachment_id ` +
		`LEFT JOIN handle ON m.handle_id = handle.ROWID ` +
		`WHERE is_from_me=0 AND m.rowid > $id ORDER BY m.date ASC`
	query, _, err := db.PrepareTransient(sql)
	if err != nil {
		return
	}
	query.SetInt64("$id", m.currentID)

	for {
		if hasRow, err := query.Step(); err != nil {
			m.ErrorLog.Printf("%s: %q\n", sql, err)
			return
		} else if !hasRow {
			m.checkErr(query.Finalize(), "query reset")
			return
		}

		// Update Current ID (for the next SELECT), and send this message to the processors.
		m.currentID = query.GetInt64("rowid")

		// Extract the text from the incoming message.
		text := strings.TrimSpace(query.GetText("text"))

		// If there is an attachment, handle it.
		var attachData []byte
		attach, attachType := query.GetText("attachment"), query.GetText("attachment_type")
		if attach != "" && attachType != "" {
			attach = strings.ReplaceAll(attach, "~", m.Config.HomePath)
			f, err := os.Open(attach)
			if err != nil {
				m.ErrorLog.Printf("failed to open file %s: %v", attach, err)
				return
			}
			defer f.Close()
			attachData, err = io.ReadAll(f)
			if err != nil {
				m.ErrorLog.Printf("failed to read data from attachment: %v", err)
				return
			}
		}

		m.inChan <- Incoming{
			RowID:          m.currentID,
			From:           strings.TrimSpace(query.GetText("handle")),
			Text:           text,
			Attachment:     attachData,
			AttachmentType: attachType,
		}
	}
}

// getCurrentID opens the iMessage DB and gets the last written / current ID.
func (m *Messages) getCurrentID() error {
	sql := `SELECT MAX(rowid) AS id FROM message`
	db, err := m.getDB()
	if err != nil {
		return err
	}
	defer m.closeDB(db)
	query, _, err := db.PrepareTransient(sql)
	if err != nil {
		return err
	}

	m.DebugLog.Print("querying current id")
	if hasrow, err := query.Step(); err != nil {
		m.ErrorLog.Printf("%s: %q\n", sql, err)
		return err
	} else if !hasrow {
		_ = query.Finalize()
		return errors.New("no message rows found")
	}
	m.currentID = query.GetInt64("id")
	return query.Finalize()
}

// handleIncoming runs the call back funcs and notifies the call back channels.
func (m *Messages) handleIncoming(msg Incoming) {
	m.DebugLog.Printf("new message id %d from: %s size: %d", msg.RowID, msg.From, len(msg.Text))
	m.binds.RLock()
	defer m.binds.RUnlock()
	// Handle call back functions.
	for _, bind := range m.Funcs {
		if matched, err := regexp.MatchString(bind.Match, msg.Text); err != nil {
			m.ErrorLog.Printf("%s: %q\n", bind.Match, err)
			continue
		} else if !matched {
			continue
		}

		m.DebugLog.Printf("found matching message handler func: %v", bind.Match)
		go bind.Func(msg)
	}
	// Handle call back channels.
	for _, bind := range m.Chans {
		if matched, err := regexp.MatchString(bind.Match, msg.Text); err != nil {
			m.ErrorLog.Printf("%s: %q\n", bind.Match, err)
			continue
		} else if !matched {
			continue
		}

		m.DebugLog.Printf("found matching message handler chan: %v", bind.Match)
		bind.Chan <- msg
	}
}
