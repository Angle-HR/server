// Package questions holds the application form question type registry.
// Each type validates its own builder config and candidate answers.
package questions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrUnknownType is returned for a question type that is not registered.
var ErrUnknownType = errors.New("questions: unknown question type")

// Type validates one kind of question.
type Type interface {
	Key() string
	// ValidateConfig runs when the employer saves the form.
	ValidateConfig(cfg json.RawMessage) error
	// ValidateAnswer runs when a candidate applies.
	ValidateAnswer(cfg, answer json.RawMessage) error
}

// Limits keep form payloads well under the API body cap.
const (
	MaxQuestionsPerForm = 50
	MaxOptions          = 50
	MinChoiceOptions    = 2
	DefaultLongTextMax  = 5000
	DefaultShortTextMax = 255
	DefaultFileMB       = 5
)

var registry = map[string]Type{}

func register(t Type) { registry[t.Key()] = t }

// Get returns the registered type for key.
func Get(key string) (Type, error) {
	t, ok := registry[key]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, key)
	}
	return t, nil
}

// Keys lists every registered type key.
func Keys() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// ValidateConfig validates cfg for the given type.
func ValidateConfig(key string, cfg json.RawMessage) error {
	t, err := Get(key)
	if err != nil {
		return err
	}
	return t.ValidateConfig(normalise(cfg))
}

// ValidateAnswer validates an answer for the given type and config.
func ValidateAnswer(key string, cfg, answer json.RawMessage) error {
	t, err := Get(key)
	if err != nil {
		return err
	}
	return t.ValidateAnswer(normalise(cfg), answer)
}

func normalise(cfg json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(cfg)) == 0 {
		return json.RawMessage(`{}`)
	}
	return cfg
}

func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}

// Option is one choice in a choice question. IDs are stable across label edits.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func validateOptions(opts []Option) error {
	if len(opts) < MinChoiceOptions || len(opts) > MaxOptions {
		return fmt.Errorf("options must have between %d and %d entries", MinChoiceOptions, MaxOptions)
	}
	seen := make(map[string]bool, len(opts))
	for i, o := range opts {
		if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" {
			return fmt.Errorf("options[%d] needs an id and a label", i)
		}
		if seen[o.ID] {
			return fmt.Errorf("options[%d] has a duplicate id", i)
		}
		seen[o.ID] = true
	}
	return nil
}

func optionSet(opts []Option) map[string]bool {
	m := make(map[string]bool, len(opts))
	for _, o := range opts {
		m[o.ID] = true
	}
	return m
}

// ---- text ----

type textConfig struct {
	MaxLength int `json:"max_length"`
}

type textType struct {
	key        string
	defaultMax int
}

func (t textType) Key() string { return t.key }

func (t textType) ValidateConfig(cfg json.RawMessage) error {
	var c textConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	if c.MaxLength < 0 {
		return errors.New("max_length must not be negative")
	}
	return nil
}

func (t textType) ValidateAnswer(cfg, answer json.RawMessage) error {
	var c textConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	var s string
	if err := json.Unmarshal(answer, &s); err != nil {
		return errors.New("answer must be a string")
	}
	max := c.MaxLength
	if max == 0 {
		max = t.defaultMax
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(s)); n > max {
		return fmt.Errorf("answer is longer than %d characters", max)
	}
	return nil
}

// ---- number ----

type numberConfig struct {
	Min  *float64 `json:"min"`
	Max  *float64 `json:"max"`
	Unit string   `json:"unit"`
}

type numberType struct{}

func (numberType) Key() string { return "number" }

func (numberType) ValidateConfig(cfg json.RawMessage) error {
	var c numberConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
		return errors.New("min must not be greater than max")
	}
	return nil
}

func (numberType) ValidateAnswer(cfg, answer json.RawMessage) error {
	var c numberConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	var n float64
	if err := json.Unmarshal(answer, &n); err != nil {
		return errors.New("answer must be a number")
	}
	if c.Min != nil && n < *c.Min {
		return fmt.Errorf("answer must be at least %v", *c.Min)
	}
	if c.Max != nil && n > *c.Max {
		return fmt.Errorf("answer must be at most %v", *c.Max)
	}
	return nil
}

// ---- email / phone / link ----

type emptyConfig struct{}

type emailType struct{}

func (emailType) Key() string { return "email" }
func (emailType) ValidateConfig(cfg json.RawMessage) error {
	return decodeStrict(cfg, &emptyConfig{})
}
func (emailType) ValidateAnswer(_, answer json.RawMessage) error {
	var s string
	if err := json.Unmarshal(answer, &s); err != nil {
		return errors.New("answer must be a string")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil || addr.Address != strings.TrimSpace(s) || !strings.Contains(addr.Address[strings.LastIndex(addr.Address, "@"):], ".") {
		return errors.New("answer must be a valid email address")
	}
	return nil
}

type phoneConfig struct {
	DefaultCountry string `json:"default_country"`
}

type phoneType struct{}

func (phoneType) Key() string { return "phone_number" }
func (phoneType) ValidateConfig(cfg json.RawMessage) error {
	var c phoneConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	if c.DefaultCountry != "" && len(c.DefaultCountry) != 2 {
		return errors.New("default_country must be a 2 letter country code")
	}
	return nil
}

// ValidateAnswer expects E.164 after the client has normalised the country picker.
func (phoneType) ValidateAnswer(_, answer json.RawMessage) error {
	var s string
	if err := json.Unmarshal(answer, &s); err != nil {
		return errors.New("answer must be a string")
	}
	if !validE164(s) {
		return errors.New("answer must be a phone number in E.164 format")
	}
	return nil
}

func validE164(s string) bool {
	if len(s) < 8 || len(s) > 16 || s[0] != '+' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type linkConfig struct {
	AllowedHosts []string `json:"allowed_hosts"`
}

type linkType struct{}

func (linkType) Key() string { return "link" }
func (linkType) ValidateConfig(cfg json.RawMessage) error {
	var c linkConfig
	return decodeStrict(cfg, &c)
}
func (linkType) ValidateAnswer(cfg, answer json.RawMessage) error {
	var c linkConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	var s string
	if err := json.Unmarshal(answer, &s); err != nil {
		return errors.New("answer must be a string")
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("answer must be an https:// URL")
	}
	if len(c.AllowedHosts) > 0 {
		host := strings.ToLower(u.Hostname())
		ok := false
		for _, h := range c.AllowedHosts {
			h = strings.ToLower(h)
			if host == h || strings.HasSuffix(host, "."+h) {
				ok = true
				break
			}
		}
		if !ok {
			return errors.New("link host is not allowed")
		}
	}
	return nil
}

// ---- choice ----

type choiceConfig struct {
	Options     []Option `json:"options"`
	MinSelected int      `json:"min_selected"`
	MaxSelected int      `json:"max_selected"`
	Searchable  bool     `json:"searchable"`
}

type choiceType struct {
	key    string
	single bool
	multi  bool // supports min/max selected
}

func (t choiceType) Key() string { return t.key }

func (t choiceType) ValidateConfig(cfg json.RawMessage) error {
	var c choiceConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	if err := validateOptions(c.Options); err != nil {
		return err
	}
	if !t.multi && (c.MinSelected != 0 || c.MaxSelected != 0) {
		return errors.New("min_selected and max_selected are not supported for this type")
	}
	if t.key != "dropdown" && c.Searchable {
		return errors.New("searchable is only supported for dropdown")
	}
	if c.MinSelected < 0 || c.MaxSelected < 0 || c.MinSelected > len(c.Options) || c.MaxSelected > len(c.Options) {
		return errors.New("min_selected and max_selected must be within the number of options")
	}
	if c.MaxSelected != 0 && c.MinSelected > c.MaxSelected {
		return errors.New("min_selected must not be greater than max_selected")
	}
	return nil
}

func (t choiceType) ValidateAnswer(cfg, answer json.RawMessage) error {
	var c choiceConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	valid := optionSet(c.Options)
	if t.single {
		var id string
		if err := json.Unmarshal(answer, &id); err != nil {
			return errors.New("answer must be one option id")
		}
		if !valid[id] {
			return errors.New("answer is not one of the options")
		}
		return nil
	}
	var ids []string
	if err := json.Unmarshal(answer, &ids); err != nil {
		return errors.New("answer must be a list of option ids")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !valid[id] {
			return errors.New("answer contains an unknown option")
		}
		if seen[id] {
			return errors.New("answer contains a duplicate option")
		}
		seen[id] = true
	}
	if len(ids) < c.MinSelected {
		return fmt.Errorf("select at least %d options", c.MinSelected)
	}
	if c.MaxSelected != 0 && len(ids) > c.MaxSelected {
		return fmt.Errorf("select at most %d options", c.MaxSelected)
	}
	return nil
}

// ---- linear scale ----

type scaleConfig struct {
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	MinLabel string `json:"min_label"`
	MaxLabel string `json:"max_label"`
}

type scaleType struct{}

func (scaleType) Key() string { return "linear_scale" }

func (scaleType) ValidateConfig(cfg json.RawMessage) error {
	var c scaleConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	if c.Min != 0 && c.Min != 1 {
		return errors.New("min must be 0 or 1")
	}
	if c.Max < 2 || c.Max > 10 {
		return errors.New("max must be between 2 and 10")
	}
	return nil
}

func (scaleType) ValidateAnswer(cfg, answer json.RawMessage) error {
	var c scaleConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	var n float64
	if err := json.Unmarshal(answer, &n); err != nil || n != float64(int(n)) {
		return errors.New("answer must be a whole number")
	}
	if int(n) < c.Min || int(n) > c.Max {
		return fmt.Errorf("answer must be between %d and %d", c.Min, c.Max)
	}
	return nil
}

// ---- uploads ----

type uploadConfig struct {
	MaxMB  int      `json:"max_mb"`
	Accept []string `json:"accept"`
}

type uploadType struct {
	key     string
	allowed map[string]bool
}

var (
	imageFormats = map[string]bool{"png": true, "jpeg": true, "webp": true}
	// SVG is never accepted: it can carry scripts and files are shown to reviewers.
	fileFormats = map[string]bool{"pdf": true, "docx": true, "odt": true}
	cvFormats   = map[string]bool{"pdf": true}
)

func (t uploadType) Key() string { return t.key }

func (t uploadType) ValidateConfig(cfg json.RawMessage) error {
	var c uploadConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	if c.MaxMB < 0 || c.MaxMB > 10 {
		return errors.New("max_mb must be between 1 and 10")
	}
	for _, a := range c.Accept {
		if !t.allowed[strings.ToLower(a)] {
			return fmt.Errorf("format %q is not accepted for this question type", a)
		}
	}
	return nil
}

// ValidateAnswer expects an upload key the server issued; the file itself goes
// straight to object storage through a presigned URL.
func (t uploadType) ValidateAnswer(_, answer json.RawMessage) error {
	var key string
	if err := json.Unmarshal(answer, &key); err != nil || strings.TrimSpace(key) == "" {
		return errors.New("answer must be an upload key")
	}
	return nil
}

// ---- date / time ----

type dateConfig struct {
	MinDate string `json:"min_date"`
	MaxDate string `json:"max_date"`
}

type dateType struct{}

func (dateType) Key() string { return "date" }

func parseBounds(c dateConfig) (min, max time.Time, err error) {
	if c.MinDate != "" {
		if min, err = time.Parse("2006-01-02", c.MinDate); err != nil {
			return min, max, errors.New("min_date must be an ISO date")
		}
	}
	if c.MaxDate != "" {
		if max, err = time.Parse("2006-01-02", c.MaxDate); err != nil {
			return min, max, errors.New("max_date must be an ISO date")
		}
	}
	return min, max, nil
}

func (dateType) ValidateConfig(cfg json.RawMessage) error {
	var c dateConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	min, max, err := parseBounds(c)
	if err != nil {
		return err
	}
	if !min.IsZero() && !max.IsZero() && min.After(max) {
		return errors.New("min_date must not be after max_date")
	}
	return nil
}

func (dateType) ValidateAnswer(cfg, answer json.RawMessage) error {
	var c dateConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return err
	}
	min, max, err := parseBounds(c)
	if err != nil {
		return err
	}
	var s string
	if err := json.Unmarshal(answer, &s); err != nil {
		return errors.New("answer must be a string")
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return errors.New("answer must be an ISO date")
	}
	if !min.IsZero() && d.Before(min) {
		return fmt.Errorf("date must be on or after %s", c.MinDate)
	}
	if !max.IsZero() && d.After(max) {
		return fmt.Errorf("date must be on or before %s", c.MaxDate)
	}
	return nil
}

type timeType struct{}

func (timeType) Key() string { return "time" }
func (timeType) ValidateConfig(cfg json.RawMessage) error {
	return decodeStrict(cfg, &emptyConfig{})
}
func (timeType) ValidateAnswer(_, answer json.RawMessage) error {
	var s string
	if err := json.Unmarshal(answer, &s); err != nil {
		return errors.New("answer must be a string")
	}
	if _, err := time.Parse("15:04", s); err != nil {
		return errors.New("answer must be a time as HH:MM")
	}
	return nil
}

func init() {
	register(textType{key: "short_text", defaultMax: DefaultShortTextMax})
	register(textType{key: "long_text", defaultMax: DefaultLongTextMax})
	register(numberType{})
	register(emailType{})
	register(phoneType{})
	register(choiceType{key: "single_choice", single: true})
	register(choiceType{key: "multiple_choice", multi: true})
	register(choiceType{key: "checkbox", multi: true})
	register(choiceType{key: "dropdown", single: true})
	register(scaleType{})
	register(uploadType{key: "image_upload", allowed: imageFormats})
	register(uploadType{key: "file_upload", allowed: fileFormats})
	register(linkType{})
	register(dateType{})
	register(timeType{})
	// Resume autofill takes the CV upload; PDF only per the PRD.
	register(uploadType{key: "autofill_resume", allowed: cvFormats})
}
