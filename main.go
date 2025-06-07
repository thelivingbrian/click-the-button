package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	datastar "github.com/starfederation/datastar/sdk/go"
)

var (
	secret       = "PASS"
	verification = "179843792357035873485623598476"
	views        = 0
	tmpl         = template.Must(template.ParseGlob("templates/*.tmpl.html"))
)

// Can generic struct like so be used for json marshal or demarshal?
type Signal map[string]any

func main() {
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.HandleFunc("/", root)
	http.HandleFunc("/guess", guess)
	http.HandleFunc("/signal", signal)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func root(w http.ResponseWriter, r *http.Request) {
	tmpl.ExecuteTemplate(w, "home", nil)
}

func signal(w http.ResponseWriter, r *http.Request) {
	var signals PageSignals
	if err := datastar.ReadSignals(r, &signals); err != nil {
		fmt.Println(err)
		return
	}
	sse := datastar.NewSSE(w, r)
	sse.MergeSignals([]byte(fmt.Sprintf(`{counter: 0, showDialog: %t}`, !signals.ShowDialog)))
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
		fmt.Println(err)
		// sse.ConsoleError(err, nil)
		return
	}

	if signals.Guess == secret {
		fmt.Println("Congrats you are a winner!")
	}

	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndMergeSignals(&signals); err != nil {
		fmt.Println(err)
		// sse.ConsoleError(err, nil)
		return
	}

}
