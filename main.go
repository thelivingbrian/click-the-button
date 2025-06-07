package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sync/atomic"

	datastar "github.com/starfederation/datastar/sdk/go"
)

var (
	tmpl   = template.Must(template.ParseGlob("templates/*.tmpl.html"))
	views  = atomic.Int64{}
	clicks = atomic.Int64{}
)

func main() {
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/", root)
	http.HandleFunc("/click", click)

	// Testing
	http.HandleFunc("/signal", signal)
	http.HandleFunc("/signal2", signal2)
	http.HandleFunc("/signal3", signal3)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func root(w http.ResponseWriter, r *http.Request) {
	views.Add(1)
	tmpl.ExecuteTemplate(w, "home", nil)
}

type PageSignals struct {
	Message    string `json:"message"`
	Counter    int    `json:"counter"`
	ShowDialog bool   `json:"showDialog"`
}

func click(w http.ResponseWriter, r *http.Request) {
	var signals PageSignals

	if err := datastar.ReadSignals(r, &signals); err != nil {
		fmt.Println(err)
		// sse.ConsoleError(err, nil)
		return
	}

	signals.Counter = int(clicks.Add(1))

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&signals); err != nil {
		fmt.Println(err)
		// sse.ConsoleError(err, nil)
		return
	}
}

////////////////////////////////////////////////////////////////
// Testing

type Signal map[string]any

func signal(w http.ResponseWriter, r *http.Request) {
	var signals PageSignals
	if err := datastar.ReadSignals(r, &signals); err != nil {
		fmt.Println(err)
		return
	}
	sse := datastar.NewSSE(w, r)
	sse.MergeSignals([]byte(`{counter: 0, showDialog: false}`))
}

func signal2(w http.ResponseWriter, r *http.Request) {
	var signals PageSignals
	if err := datastar.ReadSignals(r, &signals); err != nil {
		fmt.Println(err)
		return
	}
	sse := datastar.NewSSE(w, r)
	signals.Counter = signals.Counter + 10
	signals.ShowDialog = !signals.ShowDialog
	sse.MarshalAndMergeSignals(&signals)
}

func signal3(w http.ResponseWriter, r *http.Request) {
	signal := Signal{
		"counter": 42,
	}
	b, err := json.Marshal(signal)
	fmt.Println(b)
	if err != nil {
		// handle or log: unsupported value at path "ch"
	}
	sse := datastar.NewSSE(w, r)
	sse.MergeSignals(b)
}
