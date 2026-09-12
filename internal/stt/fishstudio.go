package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Client struct {
	APIKey     string
	APIURL     string
	HTTPClient *http.Client
}

type TranscribeResponse struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func NewClient(apiKey, apiURL string) *Client {
	if apiURL == "" {
		apiURL = "https://api.fish.audio/v1/stt"
	}
	return &Client{
		APIKey: apiKey,
		APIURL: apiURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

func (c *Client) Transcribe(ctx context.Context, audioPath string) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("FISH_STUDIO_API_KEY пустой")
	}

	f, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("open audio file: %w", err)
	}
	defer f.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}

	if _, err := io.Copy(part, f); err != nil {
		return "", fmt.Errorf("copy audio data: %w", err)
	}
	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIURL, body)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("fish studio stt request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fish studio api status %d: %s", resp.StatusCode, string(respBytes))
	}

	var res TranscribeResponse
	if err := json.Unmarshal(respBytes, &res); err != nil {
		// Fallback if response is raw text
		return string(respBytes), nil
	}

	if res.Error != "" {
		return "", fmt.Errorf("fish studio stt error: %s", res.Error)
	}

	if res.Text == "" {
		return string(respBytes), nil
	}

	return res.Text, nil
}
