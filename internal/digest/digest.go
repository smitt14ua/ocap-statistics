// Package digest extracts the small part of an OCAP recording that statistics need
// (entities, kills, hits, mission end) from the full recording JSON.
//
// A digest is a few hundred KB instead of tens of MB, so it is stored next to the
// computed statistics: when the statistics rules change, everything is recomputed
// from digests without downloading recordings from OCAP again.
package digest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Version changes when the digest format or extraction changes; old digests are then refetched.
// Version 2 added vehicle crews and the alternative event layout.
const Version = 2

type Digest struct {
	Version       int      `json:"v"`
	MissionName   string   `json:"missionName"`
	MissionAuthor string   `json:"missionAuthor,omitempty"`
	WorldName     string   `json:"worldName"`
	Tags          string   `json:"tags,omitempty"`
	CaptureDelay  float64  `json:"captureDelay"`
	EndFrame      int      `json:"endFrame"`
	MinFrame      int      `json:"minFrame"`
	MaxFrame      int      `json:"maxFrame"`
	EventCount    int      `json:"eventCount"`
	Entities      []Entity `json:"entities"`
	Kills         []Kill   `json:"kills"`
	Hits          []Hit    `json:"hits"`
	End           *End     `json:"end,omitempty"`
}

type Entity struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Side     string `json:"side,omitempty"`
	Type     string `json:"type,omitempty"`
	Class    string `json:"class,omitempty"`
	Group    string `json:"group,omitempty"`
	Role     string `json:"role,omitempty"`
	IsPlayer bool   `json:"isPlayer,omitempty"`
	Shots    int    `json:"shots,omitempty"`
	// Crew is the crew of a vehicle over time.
	Crew []CrewSpan `json:"crew,omitempty"`
}

// CrewSpan is a vehicle crew (entity ids) between two frames, inclusive.
type CrewSpan struct {
	From int   `json:"f"`
	To   int   `json:"t"`
	IDs  []int `json:"c"`
}

// CrewAt returns the crew of a vehicle at a frame.
func (e *Entity) CrewAt(frame int) []int {
	for _, s := range e.Crew {
		if frame >= s.From && frame <= s.To {
			return s.IDs
		}
	}
	return nil
}

// Kill is a "killed" event. Killer is -1 when the recording has no killer.
type Kill struct {
	Frame    int     `json:"f"`
	Victim   int     `json:"v"`
	Killer   int     `json:"k"`
	Weapon   string  `json:"w,omitempty"`
	Distance float64 `json:"d,omitempty"`
}

// Hit is a "hit" event. Shooter is -1 when unknown.
type Hit struct {
	Frame   int `json:"f"`
	Victim  int `json:"v"`
	Shooter int `json:"s"`
}

type End struct {
	Frame   int    `json:"frame"`
	Side    string `json:"side"`
	Message string `json:"message,omitempty"`
}

// Duration returns the mission length in seconds measured between the first and the last
// event (recordings start with frame -1 "connected" events, so this is about endFrame+1).
func (d *Digest) Duration() float64 {
	if d.EventCount == 0 {
		return 0
	}
	delay := d.CaptureDelay
	if delay <= 0 {
		delay = 1
	}
	return float64(d.MaxFrame-d.MinFrame) * delay
}

// Parse reads a recording JSON (version 1 format written by the OCAP extension) as a
// stream, keeping only what the digest needs.
func Parse(r io.Reader) (*Digest, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	d := &Digest{Version: Version, MinFrame: math.MaxInt, MaxFrame: math.MinInt}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		switch key {
		case "missionName":
			err = decodeString(dec, &d.MissionName)
		case "missionAuthor":
			err = decodeString(dec, &d.MissionAuthor)
		case "worldName":
			err = decodeString(dec, &d.WorldName)
		case "tags":
			err = decodeString(dec, &d.Tags)
		case "captureDelay":
			var v flexNum
			err = dec.Decode(&v)
			d.CaptureDelay = float64(v)
		case "endFrame":
			var v flexNum
			err = dec.Decode(&v)
			d.EndFrame = int(v)
		case "entities":
			err = forEachElement(dec, func() error {
				var e rawEntity
				if err := dec.Decode(&e); err != nil {
					return err
				}
				d.Entities = append(d.Entities, e.entity())
				return nil
			})
		case "events":
			err = forEachElement(dec, func() error {
				var ev []json.RawMessage
				if err := dec.Decode(&ev); err != nil {
					return err
				}
				d.addEvent(ev)
				return nil
			})
		default:
			err = skipValue(dec)
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
	}
	if d.EventCount == 0 {
		d.MinFrame, d.MaxFrame = 0, 0
	}
	return d, nil
}

func (d *Digest) addEvent(ev []json.RawMessage) {
	if len(ev) < 2 {
		return
	}
	var frame flexNum
	if json.Unmarshal(ev[0], &frame) != nil {
		return
	}
	f := int(frame)
	d.EventCount++
	d.MinFrame = min(d.MinFrame, f)
	d.MaxFrame = max(d.MaxFrame, f)

	var typ string
	if json.Unmarshal(ev[1], &typ) != nil {
		return
	}
	arg := func(i int) json.RawMessage {
		if i < len(ev) {
			return ev[i]
		}
		return nil
	}
	switch typ {
	case "killed", "hit":
		var victim, source int
		var weapon string
		var dist flexNum
		if _, alt := intOf(arg(3)); alt {
			// Alternative layout: [frame, type, sourceId, targetId, weapon, distance].
			var ok bool
			if victim, ok = intOf(arg(3)); !ok {
				return
			}
			if source, ok = intOf(arg(2)); !ok {
				source = -1
			}
			weapon = stringOf(arg(4))
			_ = json.Unmarshal(arg(5), &dist)
		} else {
			// Extension layout: [frame, type, victimId, [causedById, weapon], distance].
			var ok bool
			if victim, ok = intOf(arg(2)); !ok {
				return
			}
			source, weapon = causedBy(arg(3))
			_ = json.Unmarshal(arg(4), &dist)
		}
		if typ == "hit" {
			d.Hits = append(d.Hits, Hit{Frame: f, Victim: victim, Shooter: source})
			return
		}
		d.Kills = append(d.Kills, Kill{Frame: f, Victim: victim, Killer: source, Weapon: weapon, Distance: math.Round(float64(dist)*10) / 10})
	case "endMission":
		end := &End{Frame: f}
		var parts []json.RawMessage
		if json.Unmarshal(arg(2), &parts) == nil {
			if len(parts) > 0 {
				end.Side = stringOf(parts[0])
			}
			if len(parts) > 1 {
				end.Message = stringOf(parts[1])
			}
		} else {
			end.Side = stringOf(arg(2))
			end.Message = stringOf(arg(3))
		}
		d.End = end
	}
}

// causedBy parses [causedById, weapon]. Older recordings may contain "null" instead of an id.
func causedBy(raw json.RawMessage) (int, string) {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return -1, ""
	}
	id, ok := intOf(parts[0])
	if !ok {
		id = -1
	}
	weapon := ""
	if len(parts) > 1 {
		weapon = stringOf(parts[1])
	}
	return id, weapon
}

type rawEntity struct {
	ID          flexNum         `json:"id"`
	Name        flexStr         `json:"name"`
	Group       flexStr         `json:"group"`
	Side        flexStr         `json:"side"`
	IsPlayer    flexNum         `json:"isPlayer"`
	Type        flexStr         `json:"type"`
	Role        flexStr         `json:"role"`
	Class       flexStr         `json:"class"`
	FramesFired arrayLen        `json:"framesFired"`
	Positions   json.RawMessage `json:"positions"`
	StartFrame  flexNum         `json:"startFrameNum"`
}

func (e rawEntity) entity() Entity {
	return Entity{
		ID:       int(e.ID),
		Name:     string(e.Name),
		Side:     string(e.Side),
		Type:     string(e.Type),
		Class:    string(e.Class),
		Group:    string(e.Group),
		Role:     string(e.Role),
		IsPlayer: e.IsPlayer == 1,
		Shots:    int(e.FramesFired),
		Crew:     vehicleCrew(string(e.Type), e.Positions, int(e.StartFrame)),
	}
}

// vehicleCrew extracts the crew timeline of a vehicle from its positions. Each position is
// [pos, direction, alive, [crew ids], [fromFrame, toFrame]]; recordings without the frame
// range have one position per frame starting at startFrameNum.
func vehicleCrew(typ string, positions json.RawMessage, startFrame int) []CrewSpan {
	if typ != "vehicle" || len(positions) == 0 {
		return nil
	}
	var states []json.RawMessage
	if json.Unmarshal(positions, &states) != nil {
		return nil
	}
	var spans []CrewSpan
	for i, raw := range states {
		var st []json.RawMessage
		if json.Unmarshal(raw, &st) != nil || len(st) < 4 {
			continue
		}
		var crew []int
		var ids []json.RawMessage
		if json.Unmarshal(st[3], &ids) != nil {
			continue
		}
		for _, id := range ids {
			if v, ok := intOf(id); ok {
				crew = append(crew, v)
			}
		}
		from, to := startFrame+i, startFrame+i
		if len(st) > 4 {
			var fr []json.RawMessage
			if json.Unmarshal(st[4], &fr) == nil && len(fr) == 2 {
				a, okA := intOf(fr[0])
				b, okB := intOf(fr[1])
				if okA && okB && b >= a {
					from, to = a, b
				}
			}
		}
		if n := len(spans); n > 0 && spans[n-1].To+1 >= from && slices.Equal(spans[n-1].IDs, crew) {
			spans[n-1].To = max(spans[n-1].To, to)
			continue
		}
		if len(crew) == 0 {
			continue // empty vehicles do not matter for assists
		}
		spans = append(spans, CrewSpan{From: from, To: to, IDs: crew})
	}
	return spans
}

// Encode serialises the digest as gzipped JSON for storage.
func Encode(d *Digest) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(d); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode reverses Encode.
func Decode(b []byte) (*Digest, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var d Digest
	if err := json.NewDecoder(zr).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// --- JSON helpers -----------------------------------------------------------

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// forEachElement calls fn for every element of the array the decoder is positioned at.
// A null value is treated as an empty array.
func forEachElement(dec *json.Decoder, fn func() error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("expected array, got %v", tok)
	}
	for dec.More() {
		if err := fn(); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// skipValue consumes the next value token by token, so large unused fields (markers,
// positions) are never held in memory as a whole.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '[' || d == '{' {
				depth++
			} else {
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

func decodeString(dec *json.Decoder, dst *string) error {
	var v flexStr
	err := dec.Decode(&v)
	*dst = string(v)
	return err
}

func intOf(raw json.RawMessage) (int, bool) {
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v, true
		}
	}
	return 0, false
}

func stringOf(raw json.RawMessage) string {
	var v flexStr
	_ = json.Unmarshal(raw, &v)
	return string(v)
}

// flexNum accepts a number, a numeric string, a bool or null.
type flexNum float64

func (n *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch s {
	case "", "null", "false":
		*n = 0
		return nil
	case "true":
		*n = 1
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = flexNum(v)
	return nil
}

// flexStr accepts a string, or any other scalar converted to its text form.
type flexStr string

func (s *flexStr) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexStr(v)
		return nil
	}
	if string(b) == "null" || (len(b) > 0 && (b[0] == '[' || b[0] == '{')) {
		*s = ""
		return nil
	}
	*s = flexStr(b)
	return nil
}

// arrayLen counts the elements of a JSON array without decoding them.
type arrayLen int

func (a *arrayLen) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '[' {
		*a = 0
		return nil
	}
	depth, count, inStr, esc, empty := 0, 0, false, false, true
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			if depth == 1 {
				empty = false
			}
		case '[', '{':
			if depth == 1 {
				empty = false
			}
			depth++
		case ']', '}':
			depth--
		case ',':
			if depth == 1 {
				count++
			}
		case ' ', '\t', '\n', '\r':
		default:
			if depth == 1 {
				empty = false
			}
		}
	}
	if depth != 0 {
		return errors.New("unbalanced array")
	}
	if !empty {
		count++
	}
	*a = arrayLen(count)
	return nil
}
