package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	datastar "github.com/starfederation/datastar/sdk/go"
)

var (
	secret = "PASS"
	tmpl   = template.Must(template.ParseFiles("static/index.html"))
)

// Can generic struct like so be used for json marshal or demarshal?
type Signal map[string]any

func main() {
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", root)
	http.HandleFunc("/guess", guess)
	http.HandleFunc("/signal", signal)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func root(w http.ResponseWriter, r *http.Request) { tmpl.Execute(w, nil) }

func signal(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	sse.MergeSignals([]byte(`{counter: 7, showDialog: true}`))
}

type PageSignals struct {
	Message    string `json:"message"`
	Counter    int    `json:"counter"`
	ShowDialog bool   `json:"showDialog"`
	Guess      string `json:"guess"`
}

func guess(w http.ResponseWriter, r *http.Request) {
	var signals PageSignals

	if err := datastar.ReadSignals(r, &signals); err != nil {
		// If the JSON is malformed, send a console error back to the client.
		fmt.Println(err)
		//sse.ConsoleError(err, nil)
		return
	}
	fmt.Println(signals)
	signals.Counter = 155
	signals.ShowDialog = !signals.ShowDialog

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&signals); err != nil {
		// If something went wrong serializing, log it to the client console
		fmt.Println(err)
		//sse.ConsoleError(err, nil)
		return
	}

}
