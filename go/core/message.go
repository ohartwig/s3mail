package core

// Message is an index entry - what s3mail keeps per message so it does not
// have to fetch the message itself again.
type Message struct {
	Key           string `json:"key"`
	Mid           string `json:"mid"`
	Folder        string `json:"folder"`
	ETag          string `json:"etag"`
	Size          int64  `json:"size"`
	Date          string `json:"date"` // RFC 3339 in UTC, sortierbar als Text
	From          string `json:"from"`
	FromAddr      string `json:"from_addr"`
	To            string `json:"to"`
	Cc            string `json:"cc"`
	Subject       string `json:"subject"`
	MessageID     string `json:"message_id"`
	Snippet       string `json:"snippet"`
	HasAttachment bool   `json:"has_attachment"`
	Spam          bool   `json:"spam"`
	Virus         bool   `json:"virus"`

	// Added from the state, not part of the index.
	Read bool     `json:"read"`
	Star bool     `json:"star"`
	Tags []string `json:"tags"`
}

// Decorate attaches the state to an index entry.
func Decorate(m Message, d *Data) Message {
	e := d.Get(m.Mid)
	m.Read, m.Star = e.Read, e.Star
	m.Tags = e.Tags
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return m
}
