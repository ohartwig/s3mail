package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"s3mail/wizard"
)

func readJSON(r *http.Request, ziel any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(ziel)
}

func asInputError(err error, ziel *wizard.InputError) bool {
	return errors.As(err, ziel)
}
