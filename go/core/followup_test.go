package core

import (
	"testing"
	"time"
)

var monday = time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

func ago(days int) string {
	return monday.AddDate(0, 0, -days).Format(time.RFC3339)
}

func sent(to string, days int, subject string) Message {
	return Message{Key: "mail/sent/" + subject, Folder: Sent, Subject: subject,
		To: to, ToAddrs: []string{to}, Date: ago(days)}
}

func received(from string, days int) Message {
	return Message{Key: "mail/in-" + from + ago(days), Folder: Inbox,
		FromAddr: from, Date: ago(days)}
}

// TestWhatNobodyAnsweredComesBack - the question a business mailbox asks every
// Monday, and the reason the sent folder had to exist first.
func TestWhatNobodyAnsweredComesBack(t *testing.T) {
	msgs := []Message{
		sent("kunde@x.de", 9, "Angebot"),
		sent("partner@y.de", 3, "Nachfrage"), // not waiting long enough
		sent("bekannt@z.de", 12, "Frage"),
		received("bekannt@z.de", 10), // answered two days later
	}
	got := Unanswered(msgs, monday, 5*24*time.Hour)
	if len(got) != 1 {
		t.Fatalf("%d waiting, expected 1: %+v", len(got), got)
	}
	if got[0].Subject != "Angebot" || got[0].Days != 9 {
		t.Errorf("%+v", got[0])
	}
}

// TestAnAnswerBeforeTheMessageDoesNotCount - somebody who wrote last month has
// not answered what went out yesterday.
func TestAnAnswerBeforeTheMessageDoesNotCount(t *testing.T) {
	msgs := []Message{sent("kunde@x.de", 8, "Angebot"), received("kunde@x.de", 30)}
	if got := Unanswered(msgs, monday, 5*24*time.Hour); len(got) != 1 {
		t.Errorf("an older message counted as an answer: %+v", got)
	}
}

// TestAnAnswerFromAnyRecipientCounts - a reply often comes from a colleague of
// the person written to. Threading would be exact about the wrong question.
func TestAnAnswerFromAnyRecipientCounts(t *testing.T) {
	m := sent("chef@x.de", 8, "Angebot")
	m.ToAddrs = []string{"chef@x.de", "assistenz@x.de"}
	msgs := []Message{m, received("assistenz@x.de", 6)}
	if got := Unanswered(msgs, monday, 5*24*time.Hour); len(got) != 0 {
		t.Errorf("the colleague's answer was not counted: %+v", got)
	}
}

// TestOurOwnMailIsNoAnswer - a second message from us, or a draft, does not
// make the first one answered.
func TestOurOwnMailIsNoAnswer(t *testing.T) {
	own := Message{Key: "mail/sent/zwei", Folder: Sent, FromAddr: "post@firma.de",
		Date: ago(2), ToAddrs: []string{"kunde@x.de"}}
	draft := Message{Key: "mail/drafts/x", Folder: Drafts, FromAddr: "kunde@x.de", Date: ago(1)}
	msgs := []Message{sent("kunde@x.de", 8, "Angebot"), own, draft}
	if got := Unanswered(msgs, monday, 5*24*time.Hour); len(got) == 0 {
		t.Error("our own mail counted as an answer")
	}
}

// TestTheLongestWaitComesFirst - that is the one worth a second message.
func TestTheLongestWaitComesFirst(t *testing.T) {
	msgs := []Message{sent("a@x.de", 7, "kurz"), sent("b@x.de", 30, "lang"),
		sent("c@x.de", 14, "mittel")}
	got := Unanswered(msgs, monday, 5*24*time.Hour)
	if len(got) != 3 {
		t.Fatalf("%d waiting", len(got))
	}
	if got[0].Subject != "lang" || got[2].Subject != "kurz" {
		t.Errorf("order: %v", []string{got[0].Subject, got[1].Subject, got[2].Subject})
	}
}

// TestABrokenDateDoesNotTopplleTheList - the date comes out of a header
// somebody else wrote.
func TestABrokenDateDoesNotToppleTheList(t *testing.T) {
	bad := sent("a@x.de", 8, "kaputt")
	bad.Date = "gestern irgendwann"
	msgs := []Message{bad, sent("b@x.de", 8, "gut")}
	got := Unanswered(msgs, monday, 5*24*time.Hour)
	if len(got) != 1 || got[0].Subject != "gut" {
		t.Errorf("a broken date took the list with it: %+v", got)
	}
}

// TestTheConversationShowsBothDirections - what somebody wants in front of them
// before they pick up the phone.
func TestTheConversationShowsBothDirections(t *testing.T) {
	msgs := []Message{
		sent("kunde@x.de", 10, "Angebot"),
		received("kunde@x.de", 8),
		received("jemand@anders.de", 5),
		{Key: "mail/drafts/x", Folder: Drafts, FromAddr: "kunde@x.de", Date: ago(1)},
	}
	got := Conversation(msgs, "kunde@x.de")
	if len(got) != 2 {
		t.Fatalf("%d messages in the conversation: %+v", len(got), got)
	}
	// Newest first.
	if got[0].Date < got[1].Date {
		t.Errorf("not newest first: %v", []string{got[0].Date, got[1].Date})
	}
	if Conversation(msgs, "") != nil {
		t.Error("an empty address returned something")
	}
}
