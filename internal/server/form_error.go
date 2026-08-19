package server

import "fmt"

// formError is copy written for whoever is filling in the form: a whole
// sentence, capitalised and punctuated, rendered beside the field it is about.
//
// Go's convention that an error string reads as a lowercase fragment describes
// errors that get joined with ": " into a chain. These never are. They are the
// last word on the subject, shown once, to a person who wants to be told what
// to do next — and the distinct type is what says so, to a reader and to a
// linter alike.
type formError string

func (e formError) Error() string { return string(e) }

// formErrorf is formError for a message that has to name what went wrong.
func formErrorf(format string, args ...any) formError {
	return formError(fmt.Sprintf(format, args...))
}
