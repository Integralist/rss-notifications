package main

import (
	"bytes"         // For creating a buffer from the JSON payload
	"encoding/json" // For marshalling Go structs to JSON for Slack
	"encoding/xml"  // For parsing the RSS feed (XML)
	"fmt"           // For formatted I/O
	"io"
	"log"      // For logging messages
	"net/http" // For making HTTP GET and POST requests
	"os"       // For accessing environment variables
	"strings"  // For string manipulations
	"time"     // For setting HTTP client timeouts
)

// RSS structure definitions for XML parsing
type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Channel Channel  `xml:"channel"`
}

// Channel is the RSS channel
type Channel struct {
	XMLName xml.Name `xml:"channel"`
	Items   []Item   `xml:"item"`
}

// Item is the individual items
type Item struct {
	XMLName    xml.Name   `xml:"item"`
	Title      string     `xml:"title"`
	Link       string     `xml:"link"`
	Categories []Category `xml:"category"`
}

// Category structure to handle <![CDATA[...]]> content
type Category struct {
	XMLName xml.Name `xml:"category"` // Category element
	Data    string   `xml:",cdata"`   // The content within CDATA tags
}

// FilteredEntry is the filtered entries we want to send
type FilteredEntry struct {
	Title string `json:"title"`
	Link  string `json:"link"`
}

// SlackMessage structures the Block Kit API
// See: https://api.slack.com/block-kit
type SlackMessage struct {
	Blocks []SlackBlock `json:"blocks"` // A list of layout blocks
	Text   string       `json:"text"`   // Fallback text for notifications
}

type SlackBlock struct {
	Type string     `json:"type"`           // Type of block (e.g., "header", "section", "divider")
	Text *SlackText `json:"text,omitempty"` // Text object, used by "header" and "section"
}

type SlackText struct {
	Type  string `json:"type"`            // Type of text (e.g., "plain_text", "mrkdwn")
	Text  string `json:"text"`            // The actual text content
	Emoji bool   `json:"emoji,omitempty"` // Whether to render emojis (for plain_text)
}

// fetchAndFilterRSSEntries fetches the RSS feed, parses it, and filters for "dns" entries.
func fetchAndFilterRSSEntries(rssURL string) ([]FilteredEntry, error) {
	log.Printf("Fetching RSS feed from: %s\n", rssURL)
	var filteredEntries []FilteredEntry

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Get(rssURL)
	if err != nil {
		return nil, fmt.Errorf("error fetching RSS feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error fetching RSS feed: received status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading RSS feed body: %w", err)
	}

	var rssData RSS
	err = xml.Unmarshal(body, &rssData)
	if err != nil {
		log.Printf("XML unmarshal error. This might be due to encoding or complex CDATA. Error: %v", err)
		return nil, fmt.Errorf("error parsing XML from RSS feed: %w", err)
	}

	for _, item := range rssData.Channel.Items {
		isDNSEntry := false
		for _, cat := range item.Categories {
			if strings.TrimSpace(cat.Data) == "dns" {
				isDNSEntry = true
				break
			}
		}

		if isDNSEntry {
			if item.Link != "" {
				entryTitle := strings.TrimSpace(item.Title)
				if entryTitle == "" {
					entryTitle = "Untitled Article"
				}
				filteredEntries = append(filteredEntries, FilteredEntry{
					Title: entryTitle,
					Link:  strings.TrimSpace(item.Link),
				})
				log.Printf("Found DNS entry: '%s' - %s\n", entryTitle, item.Link)
			}
		}
	}
	return filteredEntries, nil
}

// sendNotificationToSlack sends the list of filtered entries to the Slack webhook.
func sendNotificationToSlack(webhookURL string, entries []FilteredEntry) error {
	if webhookURL == "" {
		log.Println("Error: SLACK_WEBHOOK_URL is not set. Cannot send Slack notification.")
		return fmt.Errorf("SLACK_WEBHOOK_URL is not configured")
	}

	if len(entries) == 0 {
		log.Println("No new DNS-related entries found to send to Slack.")
		return nil
	}

	// Construct Slack message using Block Kit
	blocks := []SlackBlock{
		{
			Type: "header",
			Text: &SlackText{Type: "plain_text", Text: "📰 Daily DNS News Digest (Domain Incite)", Emoji: true},
		},
		{Type: "divider"},
	}

	for _, entry := range entries {
		// Create a section block for each article link
		blocks = append(blocks, SlackBlock{
			Type: "section",
			Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("• <%s|%s>", entry.Link, entry.Title)},
		})
	}

	// Fallback text for notifications that don't support Block Kit
	fallbackText := fmt.Sprintf("%d new DNS articles from Domain Incite. First: <%s|%s>", len(entries), entries[0].Link, entries[0].Title)

	slackPayload := SlackMessage{
		Blocks: blocks,
		Text:   fallbackText,
	}

	// Marshal the Slack payload struct into JSON
	payloadBytes, err := json.Marshal(slackPayload)
	if err != nil {
		return fmt.Errorf("error marshalling Slack payload to JSON: %w", err)
	}

	log.Printf("Sending %d DNS entries to Slack...\n", len(entries))

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error sending message to Slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		responseBodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("error from Slack API with status %d: %s", resp.StatusCode, string(responseBodyBytes))
	}

	responseBody, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(responseBody)) == "ok" {
		log.Println("Successfully sent notification to Slack.")
	} else {
		log.Printf("Slack API response (Status %d): %s\n", resp.StatusCode, string(responseBody))
	}

	return nil
}

func main() {
	log.Println("Starting Go script: Fetch and filter DNS news...")

	rssURL := os.Getenv("RSS_FEED_URL")
	slackWebhookURL := os.Getenv("SLACK_WEBHOOK_URL")

	if rssURL == "" {
		log.Fatal("Critical Error: RSS_FEED_URL environment variable not set. Exiting.")
	}
	if slackWebhookURL == "" {
		log.Println("Warning: SLACK_WEBHOOK_URL environment variable not set. Slack notification will fail.")
	}

	filteredEntries, err := fetchAndFilterRSSEntries(rssURL)
	if err != nil {
		log.Fatalf("Error during RSS fetching/filtering: %v\n", err)
	}

	if len(filteredEntries) > 0 {
		log.Printf("Found %d DNS-related articles to send.\n", len(filteredEntries))
		err = sendNotificationToSlack(slackWebhookURL, filteredEntries)
		if err != nil {
			log.Fatalf("Error sending Slack notification: %v\n", err)
		}
	} else {
		log.Println("No new DNS-related articles found, or an error occurred that prevented finding any.")
	}
	log.Println("Go script finished successfully.")
}
