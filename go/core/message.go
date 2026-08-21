package core

// Message ist ein Indexeintrag - das, was s3mail pro Mail zwischenspeichert,
// ohne die Mail selbst noch einmal zu holen.
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

	// Aus dem Zustand dazugelegt, nicht Teil des Index.
	Read bool     `json:"read"`
	Star bool     `json:"star"`
	Tags []string `json:"tags"`
}

// Decorate haengt den Zustand an einen Indexeintrag.
func Decorate(m Message, d *Data) Message {
	e := d.Get(m.Mid)
	m.Read, m.Star = e.Read, e.Star
	m.Tags = e.Tags
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return m
}
