package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/joho/godotenv"
)

type Email struct {
	Subject string
	Date    time.Time
	Body    string
	Sender  string
}

type PageData struct {
	Emails []Email
}

const imapServer = "imap.mail.me.com:993"

var (
	username = "appstorereview"
	password = "453VS69wlDcueqO0"
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}
}

func connectToIMAP() (*client.Client, error) {
	if err := godotenv.Load(); err != nil {
		return nil, fmt.Errorf("error loading .env file: %v", err)
	}

	email := os.Getenv("ICLOUD_EMAIL")
	password := os.Getenv("ICLOUD_APP_PASSWORD")

	if email == "" || password == "" {
		return nil, fmt.Errorf("email or password not set in environment variables")
	}

	c, err := client.DialTLS(imapServer, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %v", err)
	}

	if err := c.Login(email, password); err != nil {
		return nil, fmt.Errorf("failed to login: %v", err)
	}

	return c, nil
}

func cleanupHTML(input string) string {
	// Remove all HTML tags
	noTags := strings.ReplaceAll(input, "</p>", "")
	noTags = strings.ReplaceAll(noTags, "<p>", "")
	noTags = strings.ReplaceAll(noTags, "</pre>", "")
	noTags = strings.ReplaceAll(noTags, "<pre>", "")
	noTags = strings.ReplaceAll(noTags, "</code>", "")
	noTags = strings.ReplaceAll(noTags, "<code>", "")
	noTags = strings.ReplaceAll(noTags, `<p style="font-size: 18px; font-weight: bold;">`, "")

	// Clean up whitespace
	return strings.TrimSpace(noTags)
}

func getMessageBody(msg *imap.Message) string {
	var body string
	for _, part := range msg.Body {
		buf := new(bytes.Buffer)
		_, err := io.Copy(buf, part)
		if err != nil {
			continue
		}

		fullBody := buf.String()

		// Debug logging
		log.Printf("Full email body: %s", fullBody)

		startMarker := "Please enter the following passcode in the app to log in:"
		endMarker := "This passcode will expire in 15 minutes."

		if startIdx := strings.Index(fullBody, startMarker); startIdx != -1 {
			// Start from the end of the start marker
			contentAfterStart := fullBody[startIdx+len(startMarker):]
			if endIdx := strings.Index(contentAfterStart, endMarker); endIdx != -1 {
				passphrase := contentAfterStart[:endIdx]
				passphrase = cleanupHTML(passphrase)
				// Debug logging
				log.Printf("Extracted passphrase: %s", passphrase)
				body = passphrase
				return body // Return first valid passcode found
			}
		}
	}
	return body
}

func getMessagesFromSender(c *client.Client, senderEmail string) ([]Email, error) {
	_, err := c.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("failed to select inbox: %v", err)
	}

	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("From", senderEmail)

	uids, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %v", err)
	}

	if len(uids) == 0 {
		return []Email{}, nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uids...)

	// Define the items we want to retrieve
	fetchItems := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchBody,
		imap.FetchBodyStructure,
		"BODY[]",
	}

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.Fetch(seqset, fetchItems, messages)
	}()

	var emails []Email
	for msg := range messages {
		body := getMessageBody(msg)

		email := Email{
			Subject: msg.Envelope.Subject,
			Date:    msg.Envelope.Date,
			Body:    body,
		}
		emails = append(emails, email)
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch messages: %v", err)
	}

	// Sort emails by date in descending order
	sort.Slice(emails, func(i, j int) bool {
		return emails[i].Date.After(emails[j].Date)
	})

	return emails, nil
}

func getFilteredMessages(c *client.Client, subject string) ([]Email, error) {
	_, err := c.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("failed to select inbox: %v", err)
	}

	// Debug: Print mailbox info
	mbox := c.Mailbox()
	log.Printf("Mailbox: %s, Messages: %d\n", mbox.Name, mbox.Messages)

	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("SUBJECT", subject)

	// Add date range from past to present
	//criteria.Since = time.Date(2025, time.January, 11, 0, 0, 0, 0, time.UTC)
	//criteria.Before = time.Now()

	uids, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %v", err)
	}

	// Debug: Print search results
	log.Printf("Found %d messages matching criteria\n", len(uids))

	if len(uids) == 0 {
		return []Email{}, nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uids...)

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.Fetch(seqset, []imap.FetchItem{
			imap.FetchEnvelope,
			imap.FetchFlags,
			imap.FetchBody,
			imap.FetchBodyStructure,
			"BODY[]",
		}, messages)
	}()

	var emails []Email
	for msg := range messages {
		// Debug: Print message date
		log.Printf("Processing message from: %v\n", msg.Envelope.Date)
		// Add debug logging for the subject
		log.Printf("Message subject: %v", msg.Envelope.Subject)

		//body := getMessageBody(msg)

		// Extract sender info
		var sender string
		if len(msg.Envelope.From) > 0 {
			from := msg.Envelope.From[0]
			if from.PersonalName != "" {
				sender = from.PersonalName
			} else {
				sender = fmt.Sprintf("%s@%s", from.MailboxName, from.HostName)
			}
		}

		email := Email{
			Subject: msg.Envelope.Subject,
			Date:    msg.Envelope.Date,
			Body:    getMessageBody(msg),
			Sender:  sender,
		}

		log.Printf("Created email: Sender=%s, Subject=%s\n", sender, email.Subject)
		emails = append(emails, email)
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch messages: %v", err)
	}

	// Sort by date descending
	sort.Slice(emails, func(i, j int) bool {
		return emails[i].Date.After(emails[j].Date)
	})

	// Debug: Print final result
	log.Printf("Returning %d processed emails\n", len(emails))

	return emails, nil
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func main() {
	tmpl := template.Must(template.ParseFiles("templates/emails.html"))

	http.HandleFunc("/", authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		c, err := connectToIMAP()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer c.Logout()

		subject := "Your Login passcode for milesAI is"

		emails, err := getFilteredMessages(c, subject)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		data := PageData{
			Emails: emails,
		}
		log.Printf("Template data: %+v", data)
		tmpl.Execute(w, data)
	}))

	fmt.Println("Server starting on :9090...")
	log.Fatal(http.ListenAndServe(":9090", nil))
}
