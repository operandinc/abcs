package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/operandinc/abcs/imessage"
	"github.com/pkg/errors"
)

var (
	listenAddr   = flag.String("listen", "localhost:11106", "address to listen on")
	endpointAddr = flag.String("endpoint", "https://example.com/abcs", "endpoint to send messages to")
)

const serverName = "abcs"

func init() {
	dsn, ok := os.LookupEnv("SENTRY_DSN")
	if ok {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:        dsn,
			ServerName: serverName,
		}); err != nil {
			log.Panic("failed to initialize sentry")
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("error: %v", err)
	}
}

type server struct {
	endpoint   string
	msgs       *imessage.Messages
	logger     imessage.Logger
	incoming   chan imessage.Incoming
	underlying *http.Server
}

var attachmentExtensions = map[string]string{
	"image/png":       "png",
	"image/jpeg":      "jpeg",
	"image/heic":      "heic",
	"application/pdf": "pdf",
	"text/vcard":      "vcf",
}

func (s *server) sendMessageHandler() http.HandlerFunc {
	type request struct {
		To             string `json:"to"`
		Message        string `json:"message"`
		Attachment     []byte `json:"attachment,omitempty"`
		AttachmentType string `json:"attachment_type,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := imessage.Outgoing{To: req.To}
		if req.Attachment != nil && req.AttachmentType != "" {
			log.Printf("(to %s) [file] %d bytes (%s)", req.To, len(req.Attachment), req.AttachmentType)
			ext, ok := attachmentExtensions[req.AttachmentType]
			if !ok {
				http.Error(w, "unknown attachment type", http.StatusBadRequest)
				return
			}
			file, err := os.CreateTemp("", fmt.Sprintf("abcs-*.%s", ext))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer file.Close()
			if _, err := file.Write(req.Attachment); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out.Text = file.Name()
			out.File = true
			out.Call = func(_ *imessage.Response) {
				os.Remove(file.Name())
			}
		} else {
			log.Printf("(to %s) %s", req.To, req.Message)
			out.Text = req.Message
		}
		s.msgs.Send(out)
		w.WriteHeader(http.StatusOK)
	}
}

type endpointRequest struct {
	From           string  `json:"from"`
	Message        string  `json:"message"`
	Attachment     []byte  `json:"attachment,omitempty"`
	AttachmentType string  `json:"attachment_type,omitempty"`
	Token          *string `json:"token,omitempty"`
}

func (s *server) notifyEndpoint(incoming imessage.Incoming) error {
	er := endpointRequest{
		From:           incoming.From,
		Message:        incoming.Text,
		Attachment:     incoming.Attachment,
		AttachmentType: incoming.AttachmentType,
	}
	buf, err := json.Marshal(er)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", s.endpoint, bytes.NewBuffer(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("endpoint returned non-200 code %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}

func (s *server) handleIncoming() {
	for msg := range s.incoming {
		if msg.Attachment != nil {
			log.Printf("(from %s) %s (w/ attachment %s - size %d)", msg.From, msg.Text, msg.AttachmentType, len(msg.Attachment))
		} else {
			log.Printf("(from %s) %s", msg.From, msg.Text)
		}
		if err := s.notifyEndpoint(msg); err != nil {
			s.logger.Printf("failed to notify endpoint: %v", err)
		}
	}
}

func currentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func getHomePath() (string, error) {
	user, err := currentUser()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/Users/%s", user), nil
}

func getiChatDBLocation() (string, error) {
	user, err := currentUser()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/Users/%s/Library/Messages/chat.db", user), nil
}

const (
	queueSize = 10 // This should be tuned to the busyness of your server.
	retries   = 3  // The number of times to retry sending a message.
)

type sentryLogger struct{}

func (sl *sentryLogger) log(err error) {
	log.Printf("(error) %v", err)
	sentry.CaptureException(err)
}

func (sl *sentryLogger) Print(v ...interface{}) {
	sl.log(errors.New(fmt.Sprint(v...)))
}

func (sl *sentryLogger) Printf(fmt string, v ...interface{}) {
	sl.log(errors.Errorf(fmt, v...))
}

func (sl *sentryLogger) Println(v ...interface{}) {
	sl.Print(v...)
}

func newServer(listen, endpoint string) (*server, error) {
	dbpath, err := getiChatDBLocation()
	if err != nil {
		return nil, err
	}
	homepath, err := getHomePath()
	if err != nil {
		return nil, err
	}
	var logger imessage.Logger = &sentryLogger{}
	c := &imessage.Config{
		SQLPath:   dbpath,
		HomePath:  homepath,
		QueueSize: queueSize,
		Retries:   retries,
		ErrorLog:  logger,
	}
	im, err := imessage.Init(c)
	if err != nil {
		return nil, err
	}
	incoming := make(chan imessage.Incoming)
	im.IncomingChan(".*", incoming)
	if err := im.Start(); err != nil {
		return nil, err
	}
	s := &server{
		endpoint: endpoint,
		msgs:     im,
		logger:   logger,
		incoming: incoming,
	}
	r := http.NewServeMux()
	r.HandleFunc("/", s.sendMessageHandler())
	s.underlying = &http.Server{
		Addr:         listen,
		Handler:      r,
		ReadTimeout:  time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go s.handleIncoming()
	return s, nil
}

func (s *server) start() error {
	return s.underlying.ListenAndServe()
}

func run() error {
	flag.Parse()
	s, err := newServer(*listenAddr, *endpointAddr)
	if err != nil {
		return err
	}
	return s.start()
}
