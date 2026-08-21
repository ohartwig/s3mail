package mimespike

import (
	"io"

	"github.com/emersion/go-message/charset"
)

// charsetReader reicht an go-message/charset durch und gibt bei unbekannter
// Kodierung den Rohstrom zurueck, statt die Mail platzen zu lassen - genauso wie
// part_text() in Python auf utf-8 mit errors="replace" zurueckfaellt.
func charsetReader(name string, input io.Reader) (io.Reader, error) {
	r, err := charset.Reader(name, input)
	if err != nil {
		return input, nil
	}
	return r, nil
}
