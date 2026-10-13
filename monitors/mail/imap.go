package mail

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/seventy-two/kitchensink/monitor"
)

// imapAccount reads a folder over IMAP, e.g. Gmail's inbox with an app
// password.
type imapAccount struct {
	name, server, user, password, folder string
	// plain dials without TLS, for tests.
	plain bool
}

func newIMAP(a Account) (*imapAccount, error) {
	acc := &imapAccount{name: a.Name, server: a.Server, user: a.User, password: strings.ReplaceAll(a.Password, " ", ""), folder: a.Folder}
	if acc.server == "" {
		acc.server = "imap.gmail.com:993"
	}
	if acc.folder == "" {
		acc.folder = "INBOX"
	}
	if acc.user == "" || acc.password == "" {
		return nil, fmt.Errorf("imap needs a user and password")
	}
	return acc, nil
}

func (a *imapAccount) Name() string { return a.name }

// imapTimeout bounds each IMAP command.
const imapTimeout = 60 * time.Second

func (a *imapAccount) Open(ctx context.Context, env monitor.Env) (session, error) {
	var (
		c   *client.Client
		err error
	)
	if a.plain {
		c, err = client.Dial(a.server)
	} else {
		c, err = client.DialTLS(a.server, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	c.Timeout = imapTimeout
	// go-imap takes no context, so closing the connection is how a
	// cancelled poll stops it.
	stop := context.AfterFunc(ctx, func() { c.Terminate() })
	if err := c.Login(a.user, a.password); err != nil {
		stop()
		c.Logout()
		return nil, fmt.Errorf("signing in: %w", err)
	}
	mbox, err := c.Select(a.folder, true)
	if err != nil {
		stop()
		c.Logout()
		return nil, fmt.Errorf("opening %s: %w", a.folder, err)
	}
	return &imapSession{c: c, stop: stop, validity: mbox.UidValidity}, nil
}

type imapSession struct {
	c        *client.Client
	stop     func() bool
	validity uint32
}

// listBatch is how many envelopes are fetched at once.
const listBatch = 500

// List's cursor is the folder's UIDVALIDITY and the last UID seen; if the
// folder's been rebuilt (a new UIDVALIDITY), it starts again from since.
func (s *imapSession) List(cursor string, since time.Time) ([]header, string, error) {
	var last uint32
	if v, u, ok := strings.Cut(cursor, ":"); ok && v == strconv.FormatUint(uint64(s.validity), 10) {
		n, _ := strconv.ParseUint(u, 10, 32)
		last = uint32(n)
	}
	crit := imap.NewSearchCriteria()
	if last > 0 {
		crit.Uid = new(imap.SeqSet)
		crit.Uid.AddRange(last+1, 0) // last+1:*
	} else {
		crit.Since = since
	}
	uids, err := s.c.UidSearch(crit)
	if err != nil {
		return nil, cursor, fmt.Errorf("searching: %w", err)
	}
	var want []uint32
	for _, u := range uids {
		if u > last { // "n:*" always includes the newest message
			want = append(want, u)
		}
	}
	next := last
	var out []header
	for i := 0; i < len(want); i += listBatch {
		set := new(imap.SeqSet)
		set.AddNum(want[i:min(i+listBatch, len(want))]...)
		ch := make(chan *imap.Message, 64)
		done := make(chan error, 1)
		go func() { done <- s.c.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope}, ch) }()
		for msg := range ch {
			if msg.Envelope == nil {
				continue
			}
			e := msg.Envelope
			id := e.MessageId
			if id == "" {
				id = fmt.Sprintf("imap:%d:%d", s.validity, msg.Uid)
			}
			out = append(out, header{
				ID: id, Ref: strconv.FormatUint(uint64(msg.Uid), 10),
				From: address(e.From), Subject: e.Subject, Date: e.Date,
			})
			next = max(next, msg.Uid)
		}
		if err := <-done; err != nil {
			return nil, cursor, fmt.Errorf("fetching: %w", err)
		}
	}
	return out, fmt.Sprintf("%d:%d", s.validity, next), nil
}

func address(as []*imap.Address) string {
	if len(as) == 0 {
		return ""
	}
	a := as[0]
	addr := a.MailboxName + "@" + a.HostName
	if a.PersonalName != "" {
		return a.PersonalName + " <" + addr + ">"
	}
	return addr
}

func (s *imapSession) Body(ref string) (string, error) {
	uid, err := strconv.ParseUint(ref, 10, 32)
	if err != nil {
		return "", err
	}
	set := new(imap.SeqSet)
	set.AddNum(uint32(uid))
	section := &imap.BodySectionName{Peek: true} // leaves it unread
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- s.c.UidFetch(set, []imap.FetchItem{section.FetchItem()}, ch) }()
	var text string
	var readErr error
	got := false
	for msg := range ch {
		if r := msg.GetBody(section); r != nil {
			got = true
			text, readErr = messageText(r)
		}
	}
	if err := <-done; err != nil {
		return "", err
	}
	if !got {
		return "", fmt.Errorf("message %s is gone", ref)
	}
	return text, readErr
}

func (s *imapSession) Close() error {
	s.stop()
	return s.c.Logout()
}
