package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"s3mail/assistent"
)

func jsonLesen(r *http.Request, ziel any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(ziel)
}

func asEingabefehler(err error, ziel *assistent.Eingabefehler) bool {
	return errors.As(err, ziel)
}
