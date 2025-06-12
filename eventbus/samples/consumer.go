package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// ANSI color codes for terminal output
const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorReset  = "\033[0m"
)

// Simplified CloudEvent structure to unmarshal the relevant fields.
type CloudEvent struct {
	SpecVersion string    `json:"specversion"`
	Type        string    `json:"type"`
	Source      string    `json:"source"`
	ID          string    `json:"id"`
	Time        time.Time `json:"time"`
	Data        LogEntry  `json:"data"`
}

// LogEntry matches the structure of the data payload within the CloudEvent.
type LogEntry struct {
	Level   string                 `json:"level"`
	Message string                 `json:"message"`
	Module  string                 `json:"module"`
	Data    map[string]interface{} `json:"data"`
}

func main() {
	// Replace this URL with your eventbus SSE endpoint if it's different.
	url := "https://logevent.icgdevspace.com/events"

	// --- Reconnection Loop ---
	// This loop will run forever, attempting to reconnect if the connection is lost.
	var reconnectDelay = 1 * time.Second
	const maxReconnectDelay = 60 * time.Second

	for {
		connectAndStream(url)

		// If connectAndStream returns, it means the connection was lost.
		log.Printf("Connection lost. Reconnecting in %v...", reconnectDelay)
		time.Sleep(reconnectDelay)

		// Implement exponential backoff for the reconnection delay.
		reconnectDelay *= 2
		if reconnectDelay > maxReconnectDelay {
			reconnectDelay = maxReconnectDelay
		}
	}
}

// connectAndStream handles the logic for a single connection attempt and streaming session.
func connectAndStream(url string) {
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("Failed to connect to SSE endpoint: %v", err)
		return // Return to the main loop to attempt reconnection.
	}
	defer resp.Body.Close()

	// Check for a non-successful HTTP status code.
	if resp.StatusCode != http.StatusOK {
		log.Printf("Received non-200 status code: %s", resp.Status)
		return
	}

	fmt.Println("Connected to SSE stream. Waiting for events...")

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// We are only interested in lines that start with "data:"
		if strings.HasPrefix(line, "data:") {
			// Trim the "data: " prefix to get the raw JSON
			jsonData := strings.TrimPrefix(line, "data: ")

			// Unmarshal the JSON into our CloudEvent struct
			var event CloudEvent
			if err := json.Unmarshal([]byte(jsonData), &event); err != nil {
				// If parsing fails, print the raw data and the error
				fmt.Printf("Error parsing event JSON: %v\nRaw data: %s\n", err, jsonData)
				continue
			}

			// Format the output to match the custom formatter
			formatLogEntry(event)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from stream: %v", err)
	}
}

// formatLogEntry takes a parsed CloudEvent and prints it in the desired format.
func formatLogEntry(event CloudEvent) {
	// Determine color based on the log level from the event data
	logData := event.Data
	levelColor := ""
	switch strings.ToLower(logData.Level) {
	case "error", "fatal", "panic":
		levelColor = ColorRed
	case "warn", "warning":
		levelColor = ColorYellow
	case "info":
		levelColor = ColorBlue
	case "debug":
		levelColor = ColorCyan
	case "trace":
		levelColor = ColorPurple
	default:
		levelColor = ColorReset
	}

	// Format the log level text (padded and uppercased)
	levelText := fmt.Sprintf("%-7s", strings.ToUpper(logData.Level))

	// Get the file path and line number from the inner 'data' map
	fileInfo := ""
	if file, ok := logData.Data["file"].(string); ok {
		fileInfo = file
	}

	// Print the final formatted string
	fmt.Printf("%s | %s%s%s | %s | file=%s\n",
		event.Time.Format("2006-01-02 15:04:05"),
		levelColor,
		levelText,
		ColorReset,
		logData.Message,
		fileInfo,
	)
}
