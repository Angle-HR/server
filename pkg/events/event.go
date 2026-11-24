// Package events is the cloud-neutral event layer of the OpenHR services.
//
// An Event is a CloudEvents 1.0 envelope (JSON structured mode) with the OpenHR extension attributes region,
// companyid and partitionkey, plus the W3C traceparent. Services publish through the Publisher port and consume
// through the Subscriber port; the adapters (memory, nats) are the only code that knows a broker. Nothing in
// this package or its adapters may import a cloud SDK (see imports_test.go).
//
// Naming convention: Event.Type is a dotted, lower-case name "<domain>.<entity>.<event>[.v<N>]" such as
// "hiring.application.submitted.v1". It doubles as the broker subject, and its first token (the domain) selects
// the stream. Event.Subject is the CloudEvents subject: the id of the entity the event is about.
package events

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SpecVersion is the CloudEvents version this package writes.
const SpecVersion = "1.0"

// Content types for Event.Data.
const (
	ContentTypeJSON  = "application/json"
	ContentTypeProto = "application/protobuf"
)

// Event is a CloudEvents 1.0 envelope.
type Event struct {
	ID              string    // unique per logical event; used for de-duplication
	Source          string    // producing service, e.g. "/services/hiring"
	Type            string    // "<domain>.<entity>.<event>[.vN]"; also the broker subject
	Subject         string    // CloudEvents subject: the entity id
	Time            time.Time // when the event happened
	DataContentType string
	Data            []byte // raw payload in DataContentType

	Region       string // OpenHR region code the data belongs to
	CompanyID    string // tenant
	PartitionKey string // events with the same key are handled in order by one consumer
	TraceParent  string // W3C traceparent
	TraceState   string // W3C tracestate

	// Extensions holds any further CloudEvents extension attributes (lower-case a-z0-9, at most 20 characters).
	Extensions map[string]string
}

var extNameRe = regexp.MustCompile(`^[a-z0-9]{1,20}$`)

// reserved are attribute names Extensions may not use.
var reserved = map[string]bool{
	"specversion": true, "id": true, "source": true, "type": true, "subject": true, "time": true,
	"datacontenttype": true, "data": true, "data_base64": true, "dataschema": true,
	"region": true, "companyid": true, "partitionkey": true, "traceparent": true, "tracestate": true,
}

// New returns an event with a fresh time-ordered id and the current time.
func New(source, typ string) Event {
	return Event{ID: uuid.Must(uuid.NewV7()).String(), Source: source, Type: typ, Time: time.Now().UTC()}
}

// NewJSON returns an event whose data is v marshalled as JSON.
func NewJSON(source, typ string, v any) (Event, error) {
	return New(source, typ).WithJSON(v)
}

// NewProto returns an event whose data is an already marshalled protobuf message. The package takes bytes so
// it does not depend on the generated contracts: call proto.Marshal at the call site.
func NewProto(source, typ string, marshalled []byte) Event {
	return New(source, typ).WithData(ContentTypeProto, marshalled)
}

// WithJSON sets the data to v marshalled as JSON.
func (e Event) WithJSON(v any) (Event, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return e, fmt.Errorf("events: marshal data: %w", err)
	}
	return e.WithData(ContentTypeJSON, b), nil
}

// WithData sets the raw payload and its media type.
func (e Event) WithData(contentType string, data []byte) Event {
	e.DataContentType, e.Data = contentType, data
	return e
}

// WithSubject sets the entity id.
func (e Event) WithSubject(s string) Event { e.Subject = s; return e }

// WithRegion sets the region code.
func (e Event) WithRegion(r string) Event { e.Region = r; return e }

// WithCompany sets the tenant id.
func (e Event) WithCompany(id string) Event { e.CompanyID = id; return e }

// WithPartitionKey sets the ordering key.
func (e Event) WithPartitionKey(k string) Event { e.PartitionKey = k; return e }

// WithExtension sets an extra extension attribute on a copy of the event.
func (e Event) WithExtension(name, value string) Event {
	m := make(map[string]string, len(e.Extensions)+1)
	for k, v := range e.Extensions {
		m[k] = v
	}
	m[name] = value
	e.Extensions = m
	return e
}

// DecodeJSON unmarshals the data into v. It fails if the content type is not JSON.
func (e Event) DecodeJSON(v any) error {
	if !isJSON(e.DataContentType) {
		return fmt.Errorf("events: data content type %q is not JSON", e.DataContentType)
	}
	return json.Unmarshal(e.Data, v)
}

// Domain is the first token of the type: the stream the event belongs to.
func (e Event) Domain() string { return Domain(e.Type) }

// Domain returns the first dot separated token of a type or subject.
func Domain(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return s
}

// Validate checks the required CloudEvents attributes and the OpenHR naming rules.
func (e Event) Validate() error {
	switch {
	case e.ID == "":
		return errors.New("events: id is required")
	case e.Source == "":
		return errors.New("events: source is required")
	case e.Type == "":
		return errors.New("events: type is required")
	}
	if err := ValidateSubject(e.Type); err != nil {
		return fmt.Errorf("events: type: %w", err)
	}
	if strings.ContainsAny(e.Type, "*>") {
		return errors.New("events: type must not contain wildcards")
	}
	if e.TraceParent != "" && !validTraceParent(e.TraceParent) {
		return errors.New("events: traceparent is malformed")
	}
	for k := range e.Extensions {
		if !extNameRe.MatchString(k) || reserved[k] {
			return fmt.Errorf("events: invalid extension name %q", k)
		}
	}
	return nil
}

// ValidateSubject checks a dotted subject or filter: non-empty tokens without spaces; ">" only last.
func ValidateSubject(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	toks := strings.Split(s, ".")
	for i, t := range toks {
		if t == "" || strings.ContainsAny(t, " \t\r\n") {
			return fmt.Errorf("bad token %q", t)
		}
		if t == ">" && i != len(toks)-1 {
			return errors.New("'>' must be the last token")
		}
	}
	return nil
}

func isJSON(ct string) bool {
	if ct == "" {
		return true // CloudEvents default
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == ContentTypeJSON || strings.HasSuffix(mt, "+json")
}

type wire map[string]json.RawMessage

func str(v string) json.RawMessage { b, _ := json.Marshal(v); return b }

// MarshalJSON writes the CloudEvents JSON structured mode.
func (e Event) MarshalJSON() ([]byte, error) {
	w := wire{
		"specversion": str(SpecVersion), "id": str(e.ID), "source": str(e.Source), "type": str(e.Type),
	}
	set := func(k, v string) {
		if v != "" {
			w[k] = str(v)
		}
	}
	set("subject", e.Subject)
	set("datacontenttype", e.DataContentType)
	set("region", e.Region)
	set("companyid", e.CompanyID)
	set("partitionkey", e.PartitionKey)
	set("traceparent", e.TraceParent)
	set("tracestate", e.TraceState)
	if !e.Time.IsZero() {
		w["time"] = str(e.Time.UTC().Format(time.RFC3339Nano))
	}
	for k, v := range e.Extensions {
		if reserved[k] {
			return nil, fmt.Errorf("events: extension %q is reserved", k)
		}
		w[k] = str(v)
	}
	if len(e.Data) > 0 {
		if isJSON(e.DataContentType) && json.Valid(e.Data) {
			w["data"] = json.RawMessage(bytes.Clone(e.Data))
		} else {
			w["data_base64"] = str(base64.StdEncoding.EncodeToString(e.Data))
		}
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the CloudEvents JSON structured mode.
func (e *Event) UnmarshalJSON(b []byte) error {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("events: decode envelope: %w", err)
	}
	var out Event
	getStr := func(k string) (string, error) {
		raw, ok := w[k]
		if !ok {
			return "", nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("events: attribute %q is not a string", k)
		}
		return s, nil
	}
	fields := []struct {
		name string
		dst  *string
	}{
		{"id", &out.ID}, {"source", &out.Source}, {"type", &out.Type}, {"subject", &out.Subject},
		{"datacontenttype", &out.DataContentType}, {"region", &out.Region}, {"companyid", &out.CompanyID},
		{"partitionkey", &out.PartitionKey}, {"traceparent", &out.TraceParent}, {"tracestate", &out.TraceState},
	}
	for _, f := range fields {
		v, err := getStr(f.name)
		if err != nil {
			return err
		}
		*f.dst = v
	}
	if sv, err := getStr("specversion"); err != nil || sv != SpecVersion {
		return fmt.Errorf("events: unsupported specversion %q", sv)
	}
	if ts, err := getStr("time"); err != nil {
		return err
	} else if ts != "" {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return fmt.Errorf("events: bad time: %w", err)
		}
		out.Time = t.UTC()
	}
	if raw, ok := w["data_base64"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return errors.New("events: data_base64 is not a string")
		}
		d, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return fmt.Errorf("events: data_base64: %w", err)
		}
		out.Data = d
	} else if raw, ok := w["data"]; ok {
		out.Data = bytes.Clone(raw)
		if out.DataContentType == "" {
			out.DataContentType = ContentTypeJSON
		}
	}
	for k, raw := range w {
		if reserved[k] {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if out.Extensions == nil {
				out.Extensions = map[string]string{}
			}
			out.Extensions[k] = s
		}
	}
	*e = out
	return nil
}

// Encode returns the structured JSON form after validating the event.
func Encode(e Event) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// Decode parses and validates the structured JSON form.
func Decode(b []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, err
	}
	return e, e.Validate()
}
