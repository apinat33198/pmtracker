package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTelegram(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" {
			http.Error(w, "bad path "+r.URL.Path, 404)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	err := Telegram{APIBase: srv.URL, BotToken: "TOKEN", ChatID: "-100123"}.Send(context.Background(), "PM digest")
	if err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "-100123" || got["text"] != "PM digest" {
		t.Errorf("payload = %v", got)
	}
}

func TestNextRun(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Date(2026, 11, 3, 7, 59, 0, 0, loc)
	n, _ := NextRun(now, "08:00", loc)
	if !n.Equal(time.Date(2026, 11, 3, 8, 0, 0, 0, loc)) {
		t.Errorf("next = %v", n)
	}
	n, _ = NextRun(now.Add(time.Minute), "08:00", loc)
	if !n.Equal(time.Date(2026, 11, 4, 8, 0, 0, 0, loc)) {
		t.Errorf("at 08:00 the next run is tomorrow, got %v", n)
	}
	if _, err := NextRun(now, "8am", loc); err == nil {
		t.Error("want an error for a bad time")
	}
}
