package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	datastar "github.com/starfederation/datastar/sdk/go"
)

type Signal map[string]any

type HomePageSignals struct {
	Message    string `json:"message"`
	Counter    int64  `json:"counter"`
	ShowDialog bool   `json:"showDialog"`
}

func (app *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	app.views.Add(1)
	signal := HomePageSignals{
		Message:    greeting,
		Counter:    app.clicks.Load(),
		ShowDialog: false,
	}

	bytes, err := json.Marshal(&signal)
	if err != nil {
		return
	}
	_ = tmpl.ExecuteTemplate(w, "home", string(bytes))
}

func (app *App) clickHandler(w http.ResponseWriter, r *http.Request) {
	count := app.clicks.Add(1)
	signal := Signal{"counter": count}

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&signal); err != nil {
		log.Println("sse:", err)
	}
}

func (app *App) streamHandler(w http.ResponseWriter, r *http.Request) {
	signal := Signal{}
	previous := int64(0)
	ticker := time.NewTicker(100 * time.Millisecond)
	sse := datastar.NewSSE(w, r)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			count := app.clicks.Load()
			if previous != count {
				previous = count
				signal["counter"] = count
				err := sse.MarshalAndMergeSignals(&signal)
				if err != nil {
					fmt.Println(err)
				}
			}
		}
	}
}
