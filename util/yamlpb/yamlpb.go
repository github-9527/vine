// MIT License
//
// Copyright (c) 2021 Lack
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

/*
Package yamlpb provides marshaling and unmarshaling between protocol buffers and YAML.
It follows the specification at https://developers.google.com/protocol-buffers/docs/proto3#json.
*/
package yamlpb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/types"
	"gopkg.in/yaml.v3"
)

const secondInNanos = int64(time.Second / time.Nanosecond)
const maxSecondsInDuration = 315576000000

// RawMessage is a raw encoded JSON value.
// It implements Marshaler and Unmarshaler and can
// be used to delay JSON decoding or precompute a JSON encoding.
type RawMessage []byte

// MarshalJSON returns m as the JSON encoding of m.
func (m RawMessage) Marshal() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}

// UnmarshalJSON sets *m to a copy of data.
func (m *RawMessage) Unmarshal(data []byte) error {
	if m == nil {
		return errors.New("json.RawMessage: UnmarshalYAML on nil pointer")
	}
	*m = append((*m)[0:0], data...)
	return nil
}

// Marshaler is a configurable object for converting between
// protocol buffer objects and a JSON representation for them.
type Marshaler struct {
	// Whether to render enum values as integers, as opposed to string values.
	EnumsAsInts bool

	// Whether to render fields with zero values.
	EmitDefaults bool

	// A string to indent each level by. The presence of this field will
	// also cause a space to appear between the field separator and
	// value, and for newlines to be appear between fields and array
	// elements.
	Indent string

	// Whether to use the original (.proto) name for fields.
	OrigName bool

	// A custom URL resolver to use when marshaling Any messages to JSON.
	// If unset, the default resolution strategy is to extract the
	// fully-qualified type name from the type URL and pass that to
	// proto.MessageType(string).
	AnyResolver AnyResolver
}

// AnyResolver takes a type URL, present in an Any message, and resolves it into
// an instance of the associated message.
type AnyResolver interface {
	Resolve(typeUrl string) (proto.Message, error)
}

func defaultResolveAny(typeUrl string) (proto.Message, error) {
	// Only the part of typeUrl after the last slash is relevant.
	mname := typeUrl
	if slash := strings.LastIndex(mname, "/"); slash >= 0 {
		mname = mname[slash+1:]
	}
	mt := proto.MessageType(mname)
	if mt == nil {
		return nil, fmt.Errorf("unknown message type %q", mname)
	}
	return reflect.New(mt.Elem()).Interface().(proto.Message), nil
}

// JSONPBMarshaler is implemented by protobuf messages that customize the
// way they are marshaled to JSON. Messages that implement this should
// also implement JSONPBUnmarshaler so that the custom format can be
// parsed.
//
// The JSON marshaling must follow the proto to JSON specification:
//	https://developers.google.com/protocol-buffers/docs/proto3#json
type JSONPBMarshaler interface {
	MarshalJSONPB(*Marshaler) ([]byte, error)
}

// JSONPBUnmarshaler is implemented by protobuf messages that customize
// the way they are unmarshaled from JSON. Messages that implement this
// should also implement JSONPBMarshaler so that the custom format can be
// produced.
//
// The JSON unmarshaling must follow the JSON to proto specification:
//	https://developers.google.com/protocol-buffers/docs/proto3#json
type JSONPBUnmarshaler interface {
	UnmarshalJSONPB(*Unmarshaler, []byte) error
}

// Marshal marshals a protocol buffer into JSON.
func (m *Marshaler) Marshal(out io.Writer, pb proto.Message) error {
	v := reflect.ValueOf(pb)
	if pb == nil || (v.Kind() == reflect.Ptr && v.IsNil()) {
		return errors.New("Marshal called with nil")
	}
	// Check for unset required fields first.
	if err := checkRequiredFields(pb); err != nil {
		return err
	}
	writer := &errWriter{writer: out}
	return m.marshalObject(writer, pb, "", "")
}

// MarshalToString converts a protocol buffer object to JSON string.
func (m *Marshaler) MarshalToString(pb proto.Message) (string, error) {
	var buf bytes.Buffer
	if err := m.Marshal(&buf, pb); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type int32Slice []int32

var nonFinite = map[string]float64{
	`"NaN"`:       math.NaN(),
	`"Infinity"`:  math.Inf(1),
	`"-Infinity"`: math.Inf(-1),
}

// For sorting extensions ids to ensure stable output.
func (s int32Slice) Len() int           { return len(s) }
func (s int32Slice) Less(i, j int) bool { return s[i] < s[j] }
func (s int32Slice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type isWkt interface {
	XXX_WellKnownType() string
}

var (
	wktType     = reflect.TypeOf((*isWkt)(nil)).Elem()
	messageType = reflect.TypeOf((*proto.Message)(nil)).Elem()
)

// marshalObject writes a struct to the Writer.
func (m *Marshaler) marshalObject(out *errWriter, v proto.Message, indent, typeURL string) error {
	if jsm, ok := v.(JSONPBMarshaler); ok {
		b, err := jsm.MarshalJSONPB(m)
		if err != nil {
			return err
		}
		if typeURL != "" {
			// we are marshaling this object to an Any type
			var js map[string]*RawMessage
			if err = yaml.Unmarshal(b, &js); err != nil {
				return fmt.Errorf("type %T produced invalid JSON: %v", v, err)
			}
			turl, err := yaml.Marshal(typeURL)
			if err != nil {
				return fmt.Errorf("failed to marshal type URL %q to JSON: %v", typeURL, err)
			}
			js["@type"] = (*RawMessage)(&turl)
			b, err = yaml.Marshal(js)
			if err != nil {
				return err
			}
		}

		out.write(string(b))
		return out.err
	}

	s := reflect.ValueOf(v).Elem()

	// Handle well-known types.
	if wkt, ok := v.(isWkt); ok {
		switch wkt.XXX_WellKnownType() {
		case "DoubleValue", "FloatValue", "Int64Value", "UInt64Value",
			"Int32Value", "UInt32Value", "BoolValue", "StringValue", "BytesValue":
			// "Wrappers use the same representation in JSON
			//  as the wrapped primitive type, ..."
			sprop := proto.GetProperties(s.Type())
			return m.marshalValue(out, sprop.Prop[0], s.Field(0), indent)
		case "Any":
			// Any is a bit more involved.
			return m.marshalAny(out, v, indent)
		case "Duration":
			s, ns := s.Field(0).Int(), s.Field(1).Int()
			if s < -maxSecondsInDuration || s > maxSecondsInDuration {
				return fmt.Errorf("seconds out of range %v", s)
			}
			if ns <= -secondInNanos || ns >= secondInNanos {
				return fmt.Errorf("ns out of range (%v, %v)", -secondInNanos, secondInNanos)
			}
			if (s > 0 && ns < 0) || (s < 0 && ns > 0) {
				return errors.New("signs of seconds and nanos do not match")
			}
			// Generated output always contains 0, 3, 6, or 9 fractional digits,
			// depending on required precision, followed by the suffix "s".
			f := "%d.%09d"
			if ns < 0 {
				ns = -ns
				if s == 0 {
					f = "-%d.%09d"
				}
			}
			x := fmt.Sprintf(f, s, ns)
			x = strings.TrimSuffix(x, "000")
			x = strings.TrimSuffix(x, "000")
			x = strings.TrimSuffix(x, ".000")
			out.write(`"`)
			out.write(x)
			out.write(`s"`)
			return out.err
		case "Struct", "ListValue":
			// Let marshalValue handle the `Struct.fields` map or the `ListValue.values` slice.
			// TODO: pass the correct Properties if needed.
			return m.marshalValue(out, &proto.Properties{}, s.Field(0), indent)
		case "Timestamp":
			// "RFC 3339, where generated output will always be Z-normalized
			//  and uses 0, 3, 6 or 9 fractional digits."
			s, ns := s.Field(0).Int(), s.Field(1).Int()
			if ns < 0 || ns >= secondInNanos {
				return fmt.Errorf("ns out of range [0, %v)", secondInNanos)
			}
			t := time.Unix(s, ns).UTC()
			// time.RFC3339Nano isn't exactly right (we need to get 3/6/9 fractional digits).
			x := t.Format("2006-01-02T15:04:05.000000000")
			x = strings.TrimSuffix(x, "000")
			x = strings.TrimSuffix(x, "000")
			x = strings.TrimSuffix(x, ".000")
			out.write(`"`)
			out.write(x)
			out.write(`Z"`)
			return out.err
		case "Value":
			// Value has a single oneof.
			kind := s.Field(0)
			if kind.IsNil() {
				// "absence of any variant indicates an error"
				return errors.New("nil Value")
			}
			// oneof -> *T -> T -> T.F
			x := kind.Elem().Elem().Field(0)
			// TODO: pass the correct Properties if needed.
			return m.marshalValue(out, &proto.Properties{}, x, indent)
		}
	}

	out.write("{")
	if m.Indent != "" {
		out.write("\n")
	}

	firstField := true

	if typeURL != "" {
		if err := m.marshalTypeURL(out, indent, typeURL); err != nil {
			return err
		}
		firstField = false
	}

	for i := 0; i < s.NumField(); i++ {
		value := s.Field(i)
		valueField := s.Type().Field(i)
		if strings.HasPrefix(valueField.Name, "XXX_") {
			continue
		}

		//this is not a protobuf field
		if valueField.Tag.Get("protobuf") == "" && valueField.Tag.Get("protobuf_oneof") == "" {
			continue
		}

		// IsNil will panic on most value kinds.
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface:
			if value.IsNil() {
				continue
			}
		}

		if !m.EmitDefaults {
			switch value.Kind() {
			case reflect.Bool:
				if !value.Bool() {
					continue
				}
			case reflect.Int32, reflect.Int64:
				if value.Int() == 0 {
					continue
				}
			case reflect.Uint32, reflect.Uint64:
				if value.Uint() == 0 {
					continue
				}
			case reflect.Float32, reflect.Float64:
				if value.Float() == 0 {
					continue
				}
			case reflect.String:
				if value.Len() == 0 {
					continue
				}
			case reflect.Map, reflect.Ptr, reflect.Slice:
				if value.IsNil() {
					continue
				}
			}
		}

		// Oneof fields need special handling.
		if valueField.Tag.Get("protobuf_oneof") != "" {
			// value is an interface containing &T{real_value}.
			sv := value.Elem().Elem() // interface -> *T -> T
			value = sv.Field(0)
			valueField = sv.Type().Field(0)
		}
		prop := yamlProperties(valueField, m.OrigName)
		if !firstField {
			m.writeSep(out)
		}
		// If the map value is a cast type, it may not implement proto.Message, therefore
		// allow the struct tag to declare the underlying message type. Change the property
		// of the child types, use CustomType as a passer. CastType currently property is
		// not used in json encoding.
		if value.Kind() == reflect.Map {
			if tag := valueField.Tag.Get("protobuf"); tag != "" {
				for _, v := range strings.Split(tag, ",") {
					if !strings.HasPrefix(v, "castvaluetype=") {
						continue
					}
					v = strings.TrimPrefix(v, "castvaluetype=")
					prop.MapValProp.CustomType = v
					break
				}
			}
		}
		if err := m.marshalField(out, prop, value, indent); err != nil {
			return err
		}
		firstField = false
	}

	// Handle proto2 extensions.
	if ep, ok := v.(proto.Message); ok {
		extensions := proto.RegisteredExtensions(v)
		// Sort extensions for stable output.
		ids := make([]int32, 0, len(extensions))
		for id, desc := range extensions {
			if !proto.HasExtension(ep, desc) {
				continue
			}
			ids = append(ids, id)
		}
		sort.Sort(int32Slice(ids))
		for _, id := range ids {
			desc := extensions[id]
			if desc == nil {
				// unknown extension
				continue
			}
			ext, extErr := proto.GetExtension(ep, desc)
			if extErr != nil {
				return extErr
			}
			value := reflect.ValueOf(ext)
			var prop proto.Properties
			prop.Parse(desc.Tag)
			prop.JSONName = fmt.Sprintf("[%s]", desc.Name)
			if !firstField {
				m.writeSep(out)
			}
			if err := m.marshalField(out, &prop, value, indent); err != nil {
				return err
			}
			firstField = false
		}

	}

	if m.Indent != "" {
		out.write("\n")
		out.write(indent)
	}
	out.write("}")
	return out.err
}

func (m *Marshaler) writeSep(out *errWriter) {
	if m.Indent != "" {
		out.write(",\n")
	} else {
		out.write(",")
	}
}

func (m *Marshaler) marshalAny(out *errWriter, any proto.Message, indent string) error {
	// "If the Any contains a value that has a special JSON mapping,
	//  it will be converted as follows: {"@type": xxx, "value": yyy}.
	//  Otherwise, the value will be converted into a JSON object,
	//  and the "@type" field will be inserted to indicate the actual data type."
	v := reflect.ValueOf(any).Elem()
	turl := v.Field(0).String()
	val := v.Field(1).Bytes()

	var msg proto.Message
	var err error
	if m.AnyResolver != nil {
		msg, err = m.AnyResolver.Resolve(turl)
	} else {
		msg, err = defaultResolveAny(turl)
	}
	if err != nil {
		return err
	}

	if err := proto.Unmarshal(val, msg); err != nil {
		return err
	}

	if _, ok := msg.(isWkt); ok {
		out.write("{")
		if m.Indent != "" {
			out.write("\n")
		}
		if err := m.marshalTypeURL(out, indent, turl); err != nil {
			return err
		}
		m.writeSep(out)
		if m.Indent != "" {
			out.write(indent)
			out.write(m.Indent)
			out.write(`"value": `)
		} else {
			out.write(`"value":`)
		}
		if err := m.marshalObject(out, msg, indent+m.Indent, ""); err != nil {
			return err
		}
		if m.Indent != "" {
			out.write("\n")
			out.write(indent)
		}
		out.write("}")
		return out.err
	}

	return m.marshalObject(out, msg, indent, turl)
}

func (m *Marshaler) marshalTypeURL(out *errWriter, indent, typeURL string) error {
	if m.Indent != "" {
		out.write(indent)
		out.write(m.Indent)
	}
	out.write(`"@type":`)
	if m.Indent != "" {
		out.write(" ")
	}
	b, err := yaml.Marshal(typeURL)
	if err != nil {
		return err
	}
	out.write(string(b))
	return out.err
}

// marshalField writes field description and value to the Writer.
func (m *Marshaler) marshalField(out *errWriter, prop *proto.Properties, v reflect.Value, indent string) error {
	if m.Indent != "" {
		out.write(indent)
		out.write(m.Indent)
	}
	out.write(`"`)
	out.write(prop.JSONName)
	out.write(`":`)
	if m.Indent != "" {
		out.write(" ")
	}
	if err := m.marshalValue(out, prop, v, indent); err != nil {
		return err
	}
	return nil
}

// marshalValue writes the value to the Writer.
func (m *Marshaler) marshalValue(out *errWriter, prop *proto.Properties, v reflect.Value, indent string) error {

	v = reflect.Indirect(v)

	// Handle nil pointer
	if v.Kind() == reflect.Invalid {
		out.write("null")
		return out.err
	}

	// Handle repeated elements.
	if v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8 {
		out.write("[")
		comma := ""
		for i := 0; i < v.Len(); i++ {
			sliceVal := v.Index(i)
			out.write(comma)
			if m.Indent != "" {
				out.write("\n")
				out.write(indent)
				out.write(m.Indent)
				out.write(m.Indent)
			}
			if err := m.marshalValue(out, prop, sliceVal, indent+m.Indent); err != nil {
				return err
			}
			comma = ","
		}
		if m.Indent != "" {
			out.write("\n")
			out.write(indent)
			out.write(m.Indent)
		}
		out.write("]")
		return out.err
	}

	// Handle well-known types.
	// Most are handled up in marshalObject (because 99% are messages).
	if v.Type().Implements(wktType) {
		wkt := v.Interface().(isWkt)
		switch wkt.XXX_WellKnownType() {
		case "NullValue":
			out.write("null")
			return out.err
		}
	}

	if t, ok := v.Interface().(time.Time); ok {
		ts, err := types.TimestampProto(t)
		if err != nil {
			return err
		}
		return m.marshalValue(out, prop, reflect.ValueOf(ts), indent)
	}

	if d, ok := v.Interface().(time.Duration); ok {
		dur := types.DurationProto(d)
		return m.marshalValue(out, prop, reflect.ValueOf(dur), indent)
	}

	// Handle enumerations.
	if !m.EnumsAsInts && prop.Enum != "" {
		// Unknown enum values will are stringified by the proto library as their
		// value. Such values should _not_ be quoted or they will be interpreted
		// as an enum string instead of their value.
		enumStr := v.Interface().(fmt.Stringer).String()
		var valStr string
		if v.Kind() == reflect.Ptr {
			valStr = strconv.Itoa(int(v.Elem().Int()))
		} else {
			valStr = strconv.Itoa(int(v.Int()))
		}

		if m, ok := v.Interface().(interface {
			MarshalJSON() ([]byte, error)
		}); ok {
			data, err := m.MarshalJSON()
			if err != nil {
				return err
			}
			enumStr = string(data)
			enumStr, err = strconv.Unquote(enumStr)
			if err != nil {
				return err
			}
		}

		isKnownEnum := enumStr != valStr

		if isKnownEnum {
			out.write(`"`)
		}
		out.write(enumStr)
		if isKnownEnum {
			out.write(`"`)
		}
		return out.err
	}

	// Handle nested messages.
	if v.Kind() == reflect.Struct {
		i := v
		if v.CanAddr() {
			i = v.Addr()
		} else {
			i = reflect.New(v.Type())
			i.Elem().Set(v)
		}
		iface := i.Interface()
		if iface == nil {
			out.write(`null`)
			return out.err
		}

		if m, ok := v.Interface().(interface {
			MarshalJSON() ([]byte, error)
		}); ok {
			data, err := m.MarshalJSON()
			if err != nil {
				return err
			}
			out.write(string(data))
			return nil
		}

		pm, ok := iface.(proto.Message)
		if !ok {
			if prop.CustomType == "" {
				return fmt.Errorf("%v does not implement proto.Message", v.Type())
			}
			t := proto.MessageType(prop.CustomType)
			if t == nil || !i.Type().ConvertibleTo(t) {
				return fmt.Errorf("%v declared custom type %s but it is not convertible to %v", v.Type(), prop.CustomType, t)
			}
			pm = i.Convert(t).Interface().(proto.Message)
		}
		return m.marshalObject(out, pm, indent+m.Indent, "")
	}

	// Handle maps.
	// Since Go randomizes map iteration, we sort keys for stable output.
	if v.Kind() == reflect.Map {
		out.write(`{`)
		keys := v.MapKeys()
		sort.Sort(mapKeys(keys))
		for i, k := range keys {
			if i > 0 {
				out.write(`,`)
			}
			if m.Indent != "" {
				out.write("\n")
				out.write(indent)
				out.write(m.Indent)
				out.write(m.Indent)
			}

			// TODO handle map key prop properly
			b, err := yaml.Marshal(k.Interface())
			if err != nil {
				return err
			}
			s := string(b)

			// If the JSON is not a string value, encode it again to make it one.
			if !strings.HasPrefix(s, `"`) {
				b, err := yaml.Marshal(s)
				if err != nil {
					return err
				}
				s = string(b)
			}

			out.write(s)
			out.write(`:`)
			if m.Indent != "" {
				out.write(` `)
			}

			vprop := prop
			if prop != nil && prop.MapValProp != nil {
				vprop = prop.MapValProp
			}
			if err := m.marshalValue(out, vprop, v.MapIndex(k), indent+m.Indent); err != nil {
				return err
			}
		}
		if m.Indent != "" {
			out.write("\n")
			out.write(indent)
			out.write(m.Indent)
		}
		out.write(`}`)
		return out.err
	}

	// Handle non-finite floats, e.g. NaN, Infinity and -Infinity.
	if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
		f := v.Float()
		var sval string
		switch {
		case math.IsInf(f, 1):
			sval = `"Infinity"`
		case math.IsInf(f, -1):
			sval = `"-Infinity"`
		case math.IsNaN(f):
			sval = `"NaN"`
		}
		if sval != "" {
			out.write(sval)
			return out.err
		}
	}

	// Default handling defers to the yaml library.
	b, err := yaml.Marshal(v.Interface())
	if err != nil {
		return err
	}
	needToQuote := string(b[0]) != `"` && (v.Kind() == reflect.Int64 || v.Kind() == reflect.Uint64)
	if needToQuote {
		out.write(`"`)
	}
	out.write(string(b))
	if needToQuote {
		out.write(`"`)
	}
	return out.err
}

// Unmarshaler is a configurable object for converting from a JSON
// representation to a protocol buffer object.
type Unmarshaler struct {
	// Whether to allow messages to contain unknown fields, as opposed to
	// failing to unmarshal.
	AllowUnknownFields bool

	// A custom URL resolver to use when unmarshaling Any messages from JSON.
	// If unset, the default resolution strategy is to extract the
	// fully-qualified type name from the type URL and pass that to
	// proto.MessageType(string).
	AnyResolver AnyResolver
}

// UnmarshalNext unmarshals the next protocol buffer from a JSON object stream.
// This function is lenient and will decode any options permutations of the
// related Marshaler.
func (u *Unmarshaler) UnmarshalNext(dec *yaml.Decoder, pb proto.Message) error {
	inputValue := RawMessage{}
	if err := dec.Decode(&inputValue); err != nil {
		return err
	}
	if err := u.unmarshalValue(reflect.ValueOf(pb).Elem(), inputValue, nil); err != nil {
		return err
	}
	return checkRequiredFields(pb)
}

// Unmarshal unmarshals a JSON object stream into a protocol
// buffer. This function is lenient and will decode any options
// permutations of the related Marshaler.
func (u *Unmarshaler) Unmarshal(r io.Reader, pb proto.Message) error {
	dec := yaml.NewDecoder(r)
	return u.UnmarshalNext(dec, pb)
}

// UnmarshalNext unmarshals the next protocol buffer from a JSON object stream.
// This function is lenient and will decode any options permutations of the
// related Marshaler.
func UnmarshalNext(dec *yaml.Decoder, pb proto.Message) error {
	u := &Unmarshaler{AllowUnknownFields: true}
	return u.UnmarshalNext(dec, pb)
}

// Unmarshal unmarshals a JSON object stream into a protocol
// buffer. This function is lenient and will decode any options
// permutations of the related Marshaler.
func Unmarshal(r io.Reader, pb proto.Message) error {
	u := &Unmarshaler{AllowUnknownFields: true}
	return u.Unmarshal(r, pb)
}

// UnmarshalString will populate the fields of a protocol buffer based
// on a JSON string. This function is lenient and will decode any options
// permutations of the related Marshaler.
func UnmarshalString(str string, pb proto.Message) error {
	u := &Unmarshaler{AllowUnknownFields: true}
	return u.Unmarshal(strings.NewReader(str), pb)
}

// unmarshalValue converts/copies a value into the target.
// prop may be nil.
func (u *Unmarshaler) unmarshalValue(target reflect.Value, inputValue RawMessage, prop *proto.Properties) error {
	targetType := target.Type()

	// Allocate memory for pointer fields.
	if targetType.Kind() == reflect.Ptr {
		// If input value is "null" and target is a pointer type, then the field should be treated as not set
		// UNLESS the target is structpb.Value, in which case it should be set to structpb.NullValue.
		_, isJSONPBUnmarshaler := target.Interface().(JSONPBUnmarshaler)
		if string(inputValue) == "null" && targetType != reflect.TypeOf(&types.Value{}) && !isJSONPBUnmarshaler {
			return nil
		}
		target.Set(reflect.New(targetType.Elem()))

		return u.unmarshalValue(target.Elem(), inputValue, prop)
	}

	if jsu, ok := target.Addr().Interface().(JSONPBUnmarshaler); ok {
		return jsu.UnmarshalJSONPB(u, []byte(inputValue))
	}

	// Handle well-known types that are not pointers.
	if w, ok := target.Addr().Interface().(isWkt); ok {
		switch w.XXX_WellKnownType() {
		case "DoubleValue", "FloatValue", "Int64Value", "UInt64Value",
			"Int32Value", "UInt32Value", "BoolValue", "StringValue", "BytesValue":
			return u.unmarshalValue(target.Field(0), inputValue, prop)
		case "Any":
			// Use RawMessage pointer type instead of value to support pre-1.8 version.
			// 1.8 changed RawMessage.MarshalJSON from pointer type to value type, see
			// https://github.com/golang/go/issues/14493
			var yamlFields map[string]*RawMessage
			if err := yaml.Unmarshal(inputValue, &yamlFields); err != nil {
				return err
			}

			val, ok := yamlFields["@type"]
			if !ok || val == nil {
				return errors.New("Any JSON doesn't have '@type'")
			}

			var turl string
			if err := yaml.Unmarshal([]byte(*val), &turl); err != nil {
				return fmt.Errorf("can't unmarshal Any's '@type': %q", *val)
			}
			target.Field(0).SetString(turl)

			var m proto.Message
			var err error
			if u.AnyResolver != nil {
				m, err = u.AnyResolver.Resolve(turl)
			} else {
				m, err = defaultResolveAny(turl)
			}
			if err != nil {
				return err
			}

			if _, ok := m.(isWkt); ok {
				val, ok := yamlFields["value"]
				if !ok {
					return errors.New("Any JSON doesn't have 'value'")
				}

				if err = u.unmarshalValue(reflect.ValueOf(m).Elem(), *val, nil); err != nil {
					return fmt.Errorf("can't unmarshal Any nested proto %T: %v", m, err)
				}
			} else {
				delete(yamlFields, "@type")
				nestedProto, uerr := yaml.Marshal(yamlFields)
				if uerr != nil {
					return fmt.Errorf("can't generate JSON for Any's nested proto to be unmarshaled: %v", uerr)
				}

				if err = u.unmarshalValue(reflect.ValueOf(m).Elem(), nestedProto, nil); err != nil {
					return fmt.Errorf("can't unmarshal Any nested proto %T: %v", m, err)
				}
			}

			b, err := proto.Marshal(m)
			if err != nil {
				return fmt.Errorf("can't marshal proto %T into Any.Value: %v", m, err)
			}
			target.Field(1).SetBytes(b)

			return nil
		case "Duration":
			unq, err := unquote(string(inputValue))
			if err != nil {
				return err
			}

			d, err := time.ParseDuration(unq)
			if err != nil {
				return fmt.Errorf("bad Duration: %v", err)
			}

			ns := d.Nanoseconds()
			s := ns / 1e9
			ns %= 1e9
			target.Field(0).SetInt(s)
			target.Field(1).SetInt(ns)
			return nil
		case "Timestamp":
			unq, err := unquote(string(inputValue))
			if err != nil {
				return err
			}

			t, err := time.Parse(time.RFC3339Nano, unq)
			if err != nil {
				return fmt.Errorf("bad Timestamp: %v", err)
			}

			target.Field(0).SetInt(t.Unix())
			target.Field(1).SetInt(int64(t.Nanosecond()))
			return nil
		case "Struct":
			var m map[string]RawMessage
			if err := yaml.Unmarshal(inputValue, &m); err != nil {
				return fmt.Errorf("bad StructValue: %v", err)
			}
			target.Field(0).Set(reflect.ValueOf(map[string]*types.Value{}))
			for k, jv := range m {
				pv := &types.Value{}
				if err := u.unmarshalValue(reflect.ValueOf(pv).Elem(), jv, prop); err != nil {
					return fmt.Errorf("bad value in StructValue for key %q: %v", k, err)
				}
				target.Field(0).SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(pv))
			}
			return nil
		case "ListValue":
			var s []RawMessage
			if err := yaml.Unmarshal(inputValue, &s); err != nil {
				return fmt.Errorf("bad ListValue: %v", err)
			}

			target.Field(0).Set(reflect.ValueOf(make([]*types.Value, len(s))))
			for i, sv := range s {
				if err := u.unmarshalValue(target.Field(0).Index(i), sv, prop); err != nil {
					return err
				}
			}
			return nil
		case "Value":
			ivStr := string(inputValue)
			if ivStr == "null" {
				target.Field(0).Set(reflect.ValueOf(&types.Value_NullValue{}))
			} else if v, err := strconv.ParseFloat(ivStr, 0); err == nil {
				target.Field(0).Set(reflect.ValueOf(&types.Value_NumberValue{NumberValue: v}))
			} else if v, err := unquote(ivStr); err == nil {
				target.Field(0).Set(reflect.ValueOf(&types.Value_StringValue{StringValue: v}))
			} else if v, err := strconv.ParseBool(ivStr); err == nil {
				target.Field(0).Set(reflect.ValueOf(&types.Value_BoolValue{BoolValue: v}))
			} else if err := yaml.Unmarshal(inputValue, &[]RawMessage{}); err == nil {
				lv := &types.ListValue{}
				target.Field(0).Set(reflect.ValueOf(&types.Value_ListValue{ListValue: lv}))
				return u.unmarshalValue(reflect.ValueOf(lv).Elem(), inputValue, prop)
			} else if err := yaml.Unmarshal(inputValue, &map[string]RawMessage{}); err == nil {
				sv := &types.Struct{}
				target.Field(0).Set(reflect.ValueOf(&types.Value_StructValue{StructValue: sv}))
				return u.unmarshalValue(reflect.ValueOf(sv).Elem(), inputValue, prop)
			} else {
				return fmt.Errorf("unrecognized type for Value %q", ivStr)
			}
			return nil
		}
	}

	if t, ok := target.Addr().Interface().(*time.Time); ok {
		ts := &types.Timestamp{}
		if err := u.unmarshalValue(reflect.ValueOf(ts).Elem(), inputValue, prop); err != nil {
			return err
		}
		tt, err := types.TimestampFromProto(ts)
		if err != nil {
			return err
		}
		*t = tt
		return nil
	}

	if d, ok := target.Addr().Interface().(*time.Duration); ok {
		dur := &types.Duration{}
		if err := u.unmarshalValue(reflect.ValueOf(dur).Elem(), inputValue, prop); err != nil {
			return err
		}
		dd, err := types.DurationFromProto(dur)
		if err != nil {
			return err
		}
		*d = dd
		return nil
	}

	// Handle enums, which have an underlying type of int32,
	// and may appear as strings.
	// The case of an enum appearing as a number is handled
	// at the bottom of this function.
	if inputValue[0] == '"' && prop != nil && prop.Enum != "" {
		vmap := proto.EnumValueMap(prop.Enum)
		// Don't need to do unquoting; valid enum names
		// are from a limited character set.
		s := inputValue[1 : len(inputValue)-1]
		n, ok := vmap[string(s)]
		if !ok {
			return fmt.Errorf("unknown value %q for enum %s", s, prop.Enum)
		}
		if target.Kind() == reflect.Ptr { // proto2
			target.Set(reflect.New(targetType.Elem()))
			target = target.Elem()
		}
		if targetType.Kind() != reflect.Int32 {
			return fmt.Errorf("invalid target %q for enum %s", targetType.Kind(), prop.Enum)
		}
		target.SetInt(int64(n))
		return nil
	}

	if prop != nil && len(prop.CustomType) > 0 && target.CanAddr() {
		if m, ok := target.Addr().Interface().(interface {
			UnmarshalJSON([]byte) error
		}); ok {
			return yaml.Unmarshal(inputValue, m)
		}
	}

	// Handle nested messages.
	if targetType.Kind() == reflect.Struct {
		var yamlFields map[string]RawMessage
		if err := yaml.Unmarshal(inputValue, &yamlFields); err != nil {
			return err
		}

		consumeField := func(prop *proto.Properties) (RawMessage, bool) {
			// Be liberal in what names we accept; both orig_name and camelName are okay.
			fieldNames := acceptedJSONFieldNames(prop)

			vOrig, okOrig := yamlFields[fieldNames.orig]
			vCamel, okCamel := yamlFields[fieldNames.camel]
			if !okOrig && !okCamel {
				return nil, false
			}
			// If, for some reason, both are present in the data, favour the camelName.
			var raw RawMessage
			if okOrig {
				raw = vOrig
				delete(yamlFields, fieldNames.orig)
			}
			if okCamel {
				raw = vCamel
				delete(yamlFields, fieldNames.camel)
			}
			return raw, true
		}

		sprops := proto.GetProperties(targetType)
		for i := 0; i < target.NumField(); i++ {
			ft := target.Type().Field(i)
			if strings.HasPrefix(ft.Name, "XXX_") {
				continue
			}
			valueForField, ok := consumeField(sprops.Prop[i])
			if !ok {
				continue
			}

			if err := u.unmarshalValue(target.Field(i), valueForField, sprops.Prop[i]); err != nil {
				return err
			}
		}
		// Check for any oneof fields.
		if len(yamlFields) > 0 {
			for _, oop := range sprops.OneofTypes {
				raw, ok := consumeField(oop.Prop)
				if !ok {
					continue
				}
				nv := reflect.New(oop.Type.Elem())
				target.Field(oop.Field).Set(nv)
				if err := u.unmarshalValue(nv.Elem().Field(0), raw, oop.Prop); err != nil {
					return err
				}
			}
		}
		// Handle proto2 extensions.
		if len(yamlFields) > 0 {
			if ep, ok := target.Addr().Interface().(proto.Message); ok {
				for _, ext := range proto.RegisteredExtensions(ep) {
					name := fmt.Sprintf("[%s]", ext.Name)
					raw, ok := yamlFields[name]
					if !ok {
						continue
					}
					delete(yamlFields, name)
					nv := reflect.New(reflect.TypeOf(ext.ExtensionType).Elem())
					if err := u.unmarshalValue(nv.Elem(), raw, nil); err != nil {
						return err
					}
					if err := proto.SetExtension(ep, ext, nv.Interface()); err != nil {
						return err
					}
				}
			}
		}
		if !u.AllowUnknownFields && len(yamlFields) > 0 {
			// Pick any field to be the scapegoat.
			var f string
			for fname := range yamlFields {
				f = fname
				break
			}
			return fmt.Errorf("unknown field %q in %v", f, targetType)
		}
		return nil
	}

	// Handle arrays
	if targetType.Kind() == reflect.Slice {
		if targetType.Elem().Kind() == reflect.Uint8 {
			outRef := reflect.New(targetType)
			outVal := outRef.Interface()
			//CustomType with underlying type []byte
			if _, ok := outVal.(interface {
				UnmarshalJSON([]byte) error
			}); ok {
				if err := yaml.Unmarshal(inputValue, outVal); err != nil {
					return err
				}
				target.Set(outRef.Elem())
				return nil
			}
			// Special case for encoded bytes. Pre-go1.5 doesn't support unmarshalling
			// strings into aliased []byte types.
			// https://github.com/golang/go/commit/4302fd0409da5e4f1d71471a6770dacdc3301197
			// https://github.com/golang/go/commit/c60707b14d6be26bf4213114d13070bff00d0b0a
			var out []byte
			if err := yaml.Unmarshal(inputValue, &out); err != nil {
				return err
			}
			target.SetBytes(out)
			return nil
		}

		var slc []RawMessage
		if err := yaml.Unmarshal(inputValue, &slc); err != nil {
			return err
		}
		if slc != nil {
			l := len(slc)
			target.Set(reflect.MakeSlice(targetType, l, l))
			for i := 0; i < l; i++ {
				if err := u.unmarshalValue(target.Index(i), slc[i], prop); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// Handle maps (whose keys are always strings)
	if targetType.Kind() == reflect.Map {
		var mp map[string]RawMessage
		if err := yaml.Unmarshal(inputValue, &mp); err != nil {
			return err
		}
		if mp != nil {
			target.Set(reflect.MakeMap(targetType))
			for ks, raw := range mp {
				// Unmarshal map key. The core yaml library already decoded the key into a
				// string, so we handle that specially. Other types were quoted post-serialization.
				var k reflect.Value
				if targetType.Key().Kind() == reflect.String {
					k = reflect.ValueOf(ks)
				} else {
					k = reflect.New(targetType.Key()).Elem()
					var kprop *proto.Properties
					if prop != nil && prop.MapKeyProp != nil {
						kprop = prop.MapKeyProp
					}
					if err := u.unmarshalValue(k, RawMessage(ks), kprop); err != nil {
						return err
					}
				}

				if !k.Type().AssignableTo(targetType.Key()) {
					k = k.Convert(targetType.Key())
				}

				// Unmarshal map value.
				v := reflect.New(targetType.Elem()).Elem()
				var vprop *proto.Properties
				if prop != nil && prop.MapValProp != nil {
					vprop = prop.MapValProp
				}
				if err := u.unmarshalValue(v, raw, vprop); err != nil {
					return err
				}
				target.SetMapIndex(k, v)
			}
		}
		return nil
	}

	// Non-finite numbers can be encoded as strings.
	isFloat := targetType.Kind() == reflect.Float32 || targetType.Kind() == reflect.Float64
	if isFloat {
		if num, ok := nonFinite[string(inputValue)]; ok {
			target.SetFloat(num)
			return nil
		}
	}

	// integers & floats can be encoded as strings. In this case we drop
	// the quotes and proceed as normal.
	isNum := targetType.Kind() == reflect.Int64 || targetType.Kind() == reflect.Uint64 ||
		targetType.Kind() == reflect.Int32 || targetType.Kind() == reflect.Uint32 ||
		targetType.Kind() == reflect.Float32 || targetType.Kind() == reflect.Float64
	if isNum && strings.HasPrefix(string(inputValue), `"`) {
		inputValue = inputValue[1 : len(inputValue)-1]
	}

	// Use the encoding/yaml for parsing other value types.
	return yaml.Unmarshal(inputValue, target.Addr().Interface())
}

func unquote(s string) (string, error) {
	var ret string
	err := yaml.Unmarshal([]byte(s), &ret)
	return ret, err
}

// yamlProperties returns parsed proto.Properties for the field and corrects JSONName attribute.
func yamlProperties(f reflect.StructField, origName bool) *proto.Properties {
	var prop proto.Properties
	prop.Init(f.Type, f.Name, f.Tag.Get("protobuf"), &f)
	if origName || prop.JSONName == "" {
		prop.JSONName = prop.OrigName
	}
	return &prop
}

type fieldNames struct {
	orig, camel string
}

func acceptedJSONFieldNames(prop *proto.Properties) fieldNames {
	opts := fieldNames{orig: prop.OrigName, camel: prop.OrigName}
	if prop.JSONName != "" {
		opts.camel = prop.JSONName
	}
	return opts
}

// Writer wrapper inspired by https://blog.golang.org/errors-are-values
type errWriter struct {
	writer io.Writer
	err    error
}

func (w *errWriter) write(str string) {
	if w.err != nil {
		return
	}
	_, w.err = w.writer.Write([]byte(str))
}

// Map fields may have key types of non-float scalars, strings and enums.
// The easiest way to sort them in some deterministic order is to use fmt.
// If this turns out to be inefficient we can always consider other options,
// such as doing a Schwartzian transform.
//
// Numeric keys are sorted in numeric order per
// https://developers.google.com/protocol-buffers/docs/proto#maps.
type mapKeys []reflect.Value

func (s mapKeys) Len() int      { return len(s) }
func (s mapKeys) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s mapKeys) Less(i, j int) bool {
	if k := s[i].Kind(); k == s[j].Kind() {
		switch k {
		case reflect.String:
			return s[i].String() < s[j].String()
		case reflect.Int32, reflect.Int64:
			return s[i].Int() < s[j].Int()
		case reflect.Uint32, reflect.Uint64:
			return s[i].Uint() < s[j].Uint()
		}
	}
	return fmt.Sprint(s[i].Interface()) < fmt.Sprint(s[j].Interface())
}

// checkRequiredFields returns an error if any required field in the given proto message is not set.
// This function is used by both Marshal and Unmarshal.  While required fields only exist in a
// proto2 message, a proto3 message can contain proto2 message(s).
func checkRequiredFields(pb proto.Message) error {
	// Most well-known type messages do not contain required fields.  The "Any" type may contain
	// a message that has required fields.
	//
	// When an Any message is being marshaled, the code will invoked proto.Unmarshal on Any.Value
	// field in order to transform that into JSON, and that should have returned an error if a
	// required field is not set in the embedded message.
	//
	// When an Any message is being unmarshaled, the code will have invoked proto.Marshal on the
	// embedded message to store the serialized message in Any.Value field, and that should have
	// returned an error if a required field is not set.
	if _, ok := pb.(isWkt); ok {
		return nil
	}

	v := reflect.ValueOf(pb)
	// Skip message if it is not a struct pointer.
	if v.Kind() != reflect.Ptr {
		return nil
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return nil
	}

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		sfield := v.Type().Field(i)

		if sfield.PkgPath != "" {
			// blank PkgPath means the field is exported; skip if not exported
			continue
		}

		if strings.HasPrefix(sfield.Name, "XXX_") {
			continue
		}

		// Oneof field is an interface implemented by wrapper structs containing the actual oneof
		// field, i.e. an interface containing &T{real_value}.
		if sfield.Tag.Get("protobuf_oneof") != "" {
			if field.Kind() != reflect.Interface {
				continue
			}
			v := field.Elem()
			if v.Kind() != reflect.Ptr || v.IsNil() {
				continue
			}
			v = v.Elem()
			if v.Kind() != reflect.Struct || v.NumField() < 1 {
				continue
			}
			field = v.Field(0)
			sfield = v.Type().Field(0)
		}

		protoTag := sfield.Tag.Get("protobuf")
		if protoTag == "" {
			continue
		}
		var prop proto.Properties
		prop.Init(sfield.Type, sfield.Name, protoTag, &sfield)

		switch field.Kind() {
		case reflect.Map:
			if field.IsNil() {
				continue
			}
			// Check each map value.
			keys := field.MapKeys()
			for _, k := range keys {
				v := field.MapIndex(k)
				if err := checkRequiredFieldsInValue(v); err != nil {
					return err
				}
			}
		case reflect.Slice:
			// Handle non-repeated type, e.g. bytes.
			if !prop.Repeated {
				if prop.Required && field.IsNil() {
					return fmt.Errorf("required field %q is not set", prop.Name)
				}
				continue
			}

			// Handle repeated type.
			if field.IsNil() {
				continue
			}
			// Check each slice item.
			for i := 0; i < field.Len(); i++ {
				v := field.Index(i)
				if err := checkRequiredFieldsInValue(v); err != nil {
					return err
				}
			}
		case reflect.Ptr:
			if field.IsNil() {
				if prop.Required {
					return fmt.Errorf("required field %q is not set", prop.Name)
				}
				continue
			}
			if err := checkRequiredFieldsInValue(field); err != nil {
				return err
			}
		}
	}

	// Handle proto2 extensions.
	for _, ext := range proto.RegisteredExtensions(pb) {
		if !proto.HasExtension(pb, ext) {
			continue
		}
		ep, err := proto.GetExtension(pb, ext)
		if err != nil {
			return err
		}
		err = checkRequiredFieldsInValue(reflect.ValueOf(ep))
		if err != nil {
			return err
		}
	}

	return nil
}

func checkRequiredFieldsInValue(v reflect.Value) error {
	if v.Type().Implements(messageType) {
		return checkRequiredFields(v.Interface().(proto.Message))
	}
	return nil
}
