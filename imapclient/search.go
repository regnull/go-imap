package imapclient

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/internal/imapwire"
)

const searchDateLayout = "2-Jan-2006"

// SearchReturnOption indicates what kind of results to return from a SEARCH
// command.
type SearchReturnOption string

const (
	SearchReturnMin   SearchReturnOption = "MIN"
	SearchReturnMax   SearchReturnOption = "MAX"
	SearchReturnAll   SearchReturnOption = "ALL"
	SearchReturnCount SearchReturnOption = "COUNT"
)

// SearchOptions contains options for the SEARCH command.
type SearchOptions struct {
	Return []SearchReturnOption // requires IMAP4rev2 or ESEARCH
}

func (c *Client) search(uid bool, criteria *SearchCriteria, options *SearchOptions) *SearchCommand {
	// TODO: use CHARSET UTF-8 with an US-ASCII fallback for IMAP4rev1 servers
	// TODO: add support for SEARCHRES
	cmd := &SearchCommand{}
	enc := c.beginCommand(uidCmdName("SEARCH", uid), cmd)
	if options != nil && len(options.Return) > 0 {
		enc.SP().Atom("RETURN").SP().List(len(options.Return), func(i int) {
			enc.Atom(string(options.Return[i]))
		})
	}
	enc.SP()
	writeSearchKey(enc.Encoder, criteria)
	enc.end()
	return cmd
}

// Search sends a SEARCH command.
func (c *Client) Search(criteria *SearchCriteria, options *SearchOptions) *SearchCommand {
	return c.search(false, criteria, options)
}

// UIDSearch sends a UID SEARCH command.
func (c *Client) UIDSearch(criteria *SearchCriteria, options *SearchOptions) *SearchCommand {
	return c.search(true, criteria, options)
}

func (c *Client) handleSearch() error {
	cmd := findPendingCmdByType[*SearchCommand](c)
	for c.dec.SP() {
		var num uint32
		if !c.dec.ExpectNumber(&num) {
			return c.dec.Err()
		}
		if cmd != nil {
			cmd.data.All.AddNum(num)
		}
	}
	return nil
}

func (c *Client) handleESearch() error {
	if !c.dec.ExpectSP() {
		return c.dec.Err()
	}
	tag, data, err := readESearchResponse(c.dec)
	if err != nil {
		return err
	}
	cmd := c.findPendingCmdFunc(func(anyCmd command) bool {
		cmd, ok := anyCmd.(*SearchCommand)
		if !ok {
			return false
		}
		if tag != "" {
			return cmd.tag == tag
		} else {
			return true
		}
	})
	if cmd != nil {
		cmd := cmd.(*SearchCommand)
		cmd.data = *data
	}
	return nil
}

// SearchCommand is a SEARCH command.
type SearchCommand struct {
	cmd
	data SearchData
}

func (cmd *SearchCommand) Wait() (*SearchData, error) {
	return &cmd.data, cmd.cmd.Wait()
}

// SearchData is the data returned by a SEARCH command.
type SearchData struct {
	All imap.SeqSet

	// requires IMAP4rev2 or ESEARCH
	UID   bool
	Min   uint32
	Max   uint32
	Count uint32
}

// AllNums returns All as a slice of numbers.
func (data *SearchData) AllNums() []uint32 {
	// Note: a dynamic sequence set would be a server bug
	nums, _ := data.All.Nums()
	return nums
}

// SearchCriteria is a criteria for the SEARCH command.
//
// When multiple fields are populated, the result is the intersection ("and"
// function) of all messages that match the fields.
type SearchCriteria struct {
	SeqNum imap.SeqSet
	UID    imap.SeqSet

	// Only the date is used, the time and timezone are ignored
	Since      time.Time
	Before     time.Time
	SentSince  time.Time
	SentBefore time.Time

	Header []SearchCriteriaHeaderField
	Body   []string
	Text   []string

	Flag    []imap.Flag
	NotFlag []imap.Flag

	Larger  int64
	Smaller int64

	Not []SearchCriteria
	Or  [][2]SearchCriteria
}

type SearchCriteriaHeaderField struct {
	Key, Value string
}

func writeSearchKey(enc *imapwire.Encoder, criteria *SearchCriteria) {
	enc.Special('(')

	firstItem := true
	encodeItem := func(s string) *imapwire.Encoder {
		if !firstItem {
			enc.SP()
		}
		firstItem = false
		return enc.Atom(s)
	}

	if len(criteria.SeqNum) > 0 {
		encodeItem(criteria.SeqNum.String())
	}
	if len(criteria.UID) > 0 {
		encodeItem("UID").SP().Atom(criteria.UID.String())
	}

	if !criteria.Since.IsZero() && !criteria.Before.IsZero() && criteria.Before.Sub(criteria.Since) == 24*time.Hour {
		encodeItem("ON").SP().String(criteria.Since.Format(searchDateLayout))
	} else {
		if !criteria.Since.IsZero() {
			encodeItem("SINCE").SP().String(criteria.Since.Format(searchDateLayout))
		}
		if !criteria.Before.IsZero() {
			encodeItem("BEFORE").SP().String(criteria.Before.Format(searchDateLayout))
		}
	}
	if !criteria.SentSince.IsZero() && !criteria.SentBefore.IsZero() && criteria.SentBefore.Sub(criteria.SentSince) == 24*time.Hour {
		encodeItem("SENTON").SP().String(criteria.SentSince.Format(searchDateLayout))
	} else {
		if !criteria.SentSince.IsZero() {
			encodeItem("SENTSINCE").SP().String(criteria.SentSince.Format(searchDateLayout))
		}
		if !criteria.SentBefore.IsZero() {
			encodeItem("SENTBEFORE").SP().String(criteria.SentBefore.Format(searchDateLayout))
		}
	}

	for _, kv := range criteria.Header {
		switch k := strings.ToUpper(kv.Key); k {
		case "BCC", "CC", "FROM", "SUBJECT", "TO":
			encodeItem(k)
		default:
			encodeItem("HEADER").SP().String(kv.Key)
		}
		enc.SP().String(kv.Value)
	}

	for _, s := range criteria.Body {
		encodeItem("BODY").SP().String(s)
	}
	for _, s := range criteria.Text {
		encodeItem("TEXT").SP().String(s)
	}

	for _, flag := range criteria.Flag {
		if k := flagSearchKey(flag); k != "" {
			encodeItem(k)
		} else {
			encodeItem("KEYWORD").SP().Flag(flag)
		}
	}
	for _, flag := range criteria.NotFlag {
		if k := flagSearchKey(flag); k != "" {
			encodeItem("UN" + k)
		} else {
			encodeItem("UNKEYWORD").SP().Flag(flag)
		}
	}

	if criteria.Larger > 0 {
		encodeItem("LARGER").SP().Number64(criteria.Larger)
	}
	if criteria.Smaller > 0 {
		encodeItem("SMALLER").SP().Number64(criteria.Smaller)
	}

	for _, not := range criteria.Not {
		encodeItem("NOT").SP()
		writeSearchKey(enc, &not)
	}
	for _, or := range criteria.Or {
		encodeItem("OR").SP()
		writeSearchKey(enc, &or[0])
		enc.SP()
		writeSearchKey(enc, &or[1])
	}

	if firstItem {
		enc.Atom("ALL")
	}

	enc.Special(')')
}

func flagSearchKey(flag imap.Flag) string {
	switch flag {
	case imap.FlagAnswered, imap.FlagDeleted, imap.FlagDraft, imap.FlagFlagged, imap.FlagSeen:
		return strings.ToUpper(strings.TrimPrefix(string(flag), "\\"))
	default:
		return ""
	}
}

func readESearchResponse(dec *imapwire.Decoder) (tag string, data *SearchData, err error) {
	data = &SearchData{}

	if dec.Special('(') { // search-correlator
		var correlator string
		if !dec.ExpectAtom(&correlator) || !dec.ExpectSP() || !dec.ExpectAString(&tag) || !dec.ExpectSpecial(')') || !dec.ExpectSP() {
			return "", nil, dec.Err()
		}
		if correlator != "TAG" {
			return "", nil, fmt.Errorf("in search-correlator: name must be TAG, but got %q", correlator)
		}
	}

	var name string
	if !dec.ExpectAtom(&name) || !dec.ExpectSP() {
		return "", nil, dec.Err()
	}
	data.UID = name == "UID"
	if data.UID {
		if !dec.ExpectAtom(&name) || !dec.ExpectSP() {
			return "", nil, dec.Err()
		}
	}
	for {
		switch returnOpt := SearchReturnOption(name); returnOpt {
		case SearchReturnMin:
			var num uint32
			if !dec.ExpectNumber(&num) {
				return "", nil, dec.Err()
			}
			data.Min = num
		case SearchReturnMax:
			var num uint32
			if !dec.ExpectNumber(&num) {
				return "", nil, dec.Err()
			}
			data.Max = num
		case SearchReturnAll:
			var s string
			if !dec.ExpectAtom(&s) {
				return "", nil, dec.Err()
			}
			data.All, err = imap.ParseSeqSet(s)
			if err != nil {
				return "", nil, err
			}
		case SearchReturnCount:
			var num uint32
			if !dec.ExpectNumber(&num) {
				return "", nil, dec.Err()
			}
			data.Count = num
		default:
			if !dec.DiscardValue() {
				return "", nil, dec.Err()
			}
		}

		if !dec.SP() {
			break
		}

		if !dec.ExpectAtom(&name) || !dec.ExpectSP() {
			return "", nil, dec.Err()
		}
	}

	return tag, data, nil
}
