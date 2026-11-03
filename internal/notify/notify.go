// Package notify sends the daily digest.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Telegram posts a message with the Bot API.
type Telegram struct {
	APIBase  string
	BotToken string
	ChatID   string
	HTTP     *http.Client
}

func (t Telegram) Send(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]any{"chat_id": t.ChatID, "text": text, "disable_web_page_preview": true})
	u := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(t.APIBase, "/"), t.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// NextRun returns the next occurrence of hh:mm in loc strictly after now.
func NextRun(now time.Time, hhmm string, loc *time.Location) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, fmt.Errorf("digest time %q: want HH:MM", hhmm)
	}
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// Daily calls fn at hh:mm local time every day until ctx ends.
func Daily(ctx context.Context, hhmm string, loc *time.Location, fn func(context.Context)) error {
	for {
		next, err := NextRun(time.Now(), hhmm, loc)
		if err != nil {
			return err
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			fn(ctx)
		}
	}
}
