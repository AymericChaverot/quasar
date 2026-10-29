package server

import (
	"log"
	"net/http"
)

// internalErrorText is what a browser is told when the server failed at
// something it should not have. It shows in the toast that reports a failed
// request.
const internalErrorText = "Something went wrong on the server. The details are in Quasar's log."

// internalError answers a request the server failed, and writes why to the
// log — not to the response. What failed is a query, a file or a call to the
// Engine, and its message names tables, paths and addresses that are nobody's
// business in a browser.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	http.Error(w, internalErrorText, http.StatusInternalServerError)
}

// apiInternalError is internalError for the JSON API.
func apiInternalError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	writeAPIError(w, http.StatusInternalServerError, "internal error")
}
