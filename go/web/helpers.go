package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"s3mail/wizard"
)

func readJSON(r *http.Request, target any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(target)
}

func asInputError(err error, target *wizard.InputError) bool {
	return errors.As(err, target)
}
