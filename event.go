package zerolog

import (
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
)

var eventPool = &sync.Pool{
	New: func() interface{} {
		return &Event{
			buf: make([]byte, 0, 500),
		}
	},
}

// Event represents a log event. It is instanced by one of the level method of
// Logger and finalized by the Msg or Msgf method.
type Event struct {
	buf   []byte
	w     LevelWriter
	level Level
	done  func(msg string)
	ch    []Hook // hooks from context
	h     []Hook
}

// LogObjectMarshaler provides a strongly-typed and encoding-agnostic interface
// to be implemented by types used with Event/Context's Object methods.
type LogObjectMarshaler interface {
	MarshalZerologObject(e *Event)
}

// LogArrayMarshaler provides a strongly-typed and encoding-agnostic interface
// to be implemented by types used with Event/Context's Array methods.
type LogArrayMarshaler interface {
	MarshalZerologArray(a *Array)
}

func newEvent(w LevelWriter, level Level, enabled bool) *Event {
	if !enabled {
		return &Event{}
	}
	e := eventPool.Get().(*Event)
	e.buf = e.buf[:1]
	e.h = e.h[:0]
	e.buf[0] = '{'
	e.w = w
	e.level = level
	return e
}

func (e *Event) write() (err error) {
	if e == nil {
		return nil
	}
	e.buf = append(e.buf, '}', '\n')
	_, err = e.w.WriteLevel(e.level, e.buf)
	eventPool.Put(e)
	return
}

// Enabled return false if the *Event is going to be filtered out by
// log level or sampling.
func (e *Event) Enabled() bool {
	return e != nil
}

// Msg sends the *Event with msg added as the message field if not empty.
//
// NOTICE: once this method is called, the *Event should be disposed.
// Calling Msg twice can have unexpected result.
func (e *Event) Msg(msg string) {
	if e == nil {
		return
	}
	if len(e.ch) > 0 {
		e.ch[0].Run(e, e.level, msg)
		if len(e.ch) > 1 {
			for _, hook := range e.ch[1:] {
				hook.Run(e, e.level, msg)
			}
		}
	}
	if len(e.h) > 0 {
		e.h[0].Run(e, e.level, msg)
		if len(e.h) > 1 {
			for _, hook := range e.h[1:] {
				hook.Run(e, e.level, msg)
			}
		}
	}
	if msg != "" {
		e.buf = appendString(appendKey(e.buf, MessageFieldName), msg)
	}
	if e.done != nil {
		defer e.done(msg)
	}
	if err := e.write(); err != nil {
		fmt.Fprintf(os.Stderr, "zerolog: could not write event: %v", err)
	}
}

// Msgf sends the event with formated msg added as the message field if not empty.
//
// NOTICE: once this methid is called, the *Event should be disposed.
// Calling Msg twice can have unexpected result.
func (e *Event) Msgf(format string, v ...interface{}) {
	if e == nil {
		return
	}
	e.Msg(fmt.Sprintf(format, v...))
}

// Fields is a helper function to use a map to set fields using type assertion.
func (e *Event) Fields(fields map[string]interface{}) *Event {
	if e == nil {
		return e
	}
	e.buf = appendFields(e.buf, fields)
	return e
}

// Dict adds the field key with a dict to the event context.
// Use zerolog.Dict() to create the dictionary.
func (e *Event) Dict(key string, dict *Event) *Event {
	if e == nil {
		return e
	}
	e.buf = append(append(appendKey(e.buf, key), dict.buf...), '}')
	eventPool.Put(dict)
	return e
}

// Dict creates an Event to be used with the *Event.Dict method.
// Call usual field methods like Str, Int etc to add fields to this
// event and give it as argument the *Event.Dict method.
func Dict() *Event {
	return newEvent(levelWriterAdapter{ioutil.Discard}, 0, true)
}

// Array adds the field key with an array to the event context.
// Use zerolog.Arr() to create the array or pass a type that
// implement the LogArrayMarshaler interface.
func (e *Event) Array(key string, arr LogArrayMarshaler) *Event {
	if e == nil {
		return e
	}
	e.buf = appendKey(e.buf, key)
	var a *Array
	if aa, ok := arr.(*Array); ok {
		a = aa
	} else {
		a = Arr()
		arr.MarshalZerologArray(a)
	}
	e.buf = a.write(e.buf)
	return e
}

func (e *Event) appendObject(obj LogObjectMarshaler) {
	pos := len(e.buf)
	obj.MarshalZerologObject(e)
	if pos < len(e.buf) {
		// As MarshalZerologObject will use event API, the first field will be
		// preceded by a comma. If at least one field has been added (buf grew),
		// we replace this coma by the opening bracket.
		e.buf[pos] = '{'
	} else {
		e.buf = append(e.buf, '{')
	}
	e.buf = append(e.buf, '}')
}

// Object marshals an object that implement the LogObjectMarshaler interface.
func (e *Event) Object(key string, obj LogObjectMarshaler) *Event {
	if e == nil {
		return e
	}
	e.buf = appendKey(e.buf, key)
	e.appendObject(obj)
	return e
}

// Str adds the field key with val as a string to the *Event context.
func (e *Event) Str(key, val string) *Event {
	if e == nil {
		return e
	}
	e.buf = appendString(appendKey(e.buf, key), val)
	return e
}

// Strs adds the field key with vals as a []string to the *Event context.
func (e *Event) Strs(key string, vals []string) *Event {
	if e == nil {
		return e
	}
	e.buf = appendStrings(appendKey(e.buf, key), vals)
	return e
}

// Bytes adds the field key with val as a string to the *Event context.
//
// Runes outside of normal ASCII ranges will be hex-encoded in the resulting
// JSON.
func (e *Event) Bytes(key string, val []byte) *Event {
	if e == nil {
		return e
	}
	e.buf = appendBytes(appendKey(e.buf, key), val)
	return e
}

// AnErr adds the field key with err as a string to the *Event context.
// If err is nil, no field is added.
func (e *Event) AnErr(key string, err error) *Event {
	if e == nil {
		return e
	}
	if err != nil {
		e.buf = appendError(appendKey(e.buf, key), err)
	}
	return e
}

// Errs adds the field key with errs as an array of strings to the *Event context.
// If err is nil, no field is added.
func (e *Event) Errs(key string, errs []error) *Event {
	if e == nil {
		return e
	}
	e.buf = appendErrors(appendKey(e.buf, key), errs)
	return e
}

// Err adds the field "error" with err as a string to the *Event context.
// If err is nil, no field is added.
// To customize the key name, change zerolog.ErrorFieldName.
func (e *Event) Err(err error) *Event {
	if e == nil {
		return e
	}
	if err != nil {
		e.buf = appendError(appendKey(e.buf, ErrorFieldName), err)
	}
	return e
}

// Bool adds the field key with val as a bool to the *Event context.
func (e *Event) Bool(key string, b bool) *Event {
	if e == nil {
		return e
	}
	e.buf = appendBool(appendKey(e.buf, key), b)
	return e
}

// Bools adds the field key with val as a []bool to the *Event context.
func (e *Event) Bools(key string, b []bool) *Event {
	if e == nil {
		return e
	}
	e.buf = appendBools(appendKey(e.buf, key), b)
	return e
}

// Int adds the field key with i as a int to the *Event context.
func (e *Event) Int(key string, i int) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInt(appendKey(e.buf, key), i)
	return e
}

// Ints adds the field key with i as a []int to the *Event context.
func (e *Event) Ints(key string, i []int) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInts(appendKey(e.buf, key), i)
	return e
}

// Int8 adds the field key with i as a int8 to the *Event context.
func (e *Event) Int8(key string, i int8) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInt8(appendKey(e.buf, key), i)
	return e
}

// Ints8 adds the field key with i as a []int8 to the *Event context.
func (e *Event) Ints8(key string, i []int8) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInts8(appendKey(e.buf, key), i)
	return e
}

// Int16 adds the field key with i as a int16 to the *Event context.
func (e *Event) Int16(key string, i int16) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInt16(appendKey(e.buf, key), i)
	return e
}

// Ints16 adds the field key with i as a []int16 to the *Event context.
func (e *Event) Ints16(key string, i []int16) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInts16(appendKey(e.buf, key), i)
	return e
}

// Int32 adds the field key with i as a int32 to the *Event context.
func (e *Event) Int32(key string, i int32) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInt32(appendKey(e.buf, key), i)
	return e
}

// Ints32 adds the field key with i as a []int32 to the *Event context.
func (e *Event) Ints32(key string, i []int32) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInts32(appendKey(e.buf, key), i)
	return e
}

// Int64 adds the field key with i as a int64 to the *Event context.
func (e *Event) Int64(key string, i int64) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInt64(appendKey(e.buf, key), i)
	return e
}

// Ints64 adds the field key with i as a []int64 to the *Event context.
func (e *Event) Ints64(key string, i []int64) *Event {
	if e == nil {
		return e
	}
	e.buf = appendInts64(appendKey(e.buf, key), i)
	return e
}

// Uint adds the field key with i as a uint to the *Event context.
func (e *Event) Uint(key string, i uint) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUint(appendKey(e.buf, key), i)
	return e
}

// Uints adds the field key with i as a []int to the *Event context.
func (e *Event) Uints(key string, i []uint) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUints(appendKey(e.buf, key), i)
	return e
}

// Uint8 adds the field key with i as a uint8 to the *Event context.
func (e *Event) Uint8(key string, i uint8) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUint8(appendKey(e.buf, key), i)
	return e
}

// Uints8 adds the field key with i as a []int8 to the *Event context.
func (e *Event) Uints8(key string, i []uint8) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUints8(appendKey(e.buf, key), i)
	return e
}

// Uint16 adds the field key with i as a uint16 to the *Event context.
func (e *Event) Uint16(key string, i uint16) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUint16(appendKey(e.buf, key), i)
	return e
}

// Uints16 adds the field key with i as a []int16 to the *Event context.
func (e *Event) Uints16(key string, i []uint16) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUints16(appendKey(e.buf, key), i)
	return e
}

// Uint32 adds the field key with i as a uint32 to the *Event context.
func (e *Event) Uint32(key string, i uint32) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUint32(appendKey(e.buf, key), i)
	return e
}

// Uints32 adds the field key with i as a []int32 to the *Event context.
func (e *Event) Uints32(key string, i []uint32) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUints32(appendKey(e.buf, key), i)
	return e
}

// Uint64 adds the field key with i as a uint64 to the *Event context.
func (e *Event) Uint64(key string, i uint64) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUint64(appendKey(e.buf, key), i)
	return e
}

// Uints64 adds the field key with i as a []int64 to the *Event context.
func (e *Event) Uints64(key string, i []uint64) *Event {
	if e == nil {
		return e
	}
	e.buf = appendUints64(appendKey(e.buf, key), i)
	return e
}

// Float32 adds the field key with f as a float32 to the *Event context.
func (e *Event) Float32(key string, f float32) *Event {
	if e == nil {
		return e
	}
	e.buf = appendFloat32(appendKey(e.buf, key), f)
	return e
}

// Floats32 adds the field key with f as a []float32 to the *Event context.
func (e *Event) Floats32(key string, f []float32) *Event {
	if e == nil {
		return e
	}
	e.buf = appendFloats32(appendKey(e.buf, key), f)
	return e
}

// Float64 adds the field key with f as a float64 to the *Event context.
func (e *Event) Float64(key string, f float64) *Event {
	if e == nil {
		return e
	}
	e.buf = appendFloat64(appendKey(e.buf, key), f)
	return e
}

// Floats64 adds the field key with f as a []float64 to the *Event context.
func (e *Event) Floats64(key string, f []float64) *Event {
	if e == nil {
		return e
	}
	e.buf = appendFloats64(appendKey(e.buf, key), f)
	return e
}

// Timestamp adds the current local time as UNIX timestamp to the *Event context with the "time" key.
// To customize the key name, change zerolog.TimestampFieldName.
func (e *Event) Timestamp() *Event {
	if e == nil {
		return e
	}
	e.buf = appendTime(appendKey(e.buf, TimestampFieldName), TimestampFunc(), TimeFieldFormat)
	return e
}

// Time adds the field key with t formated as string using zerolog.TimeFieldFormat.
func (e *Event) Time(key string, t time.Time) *Event {
	if e == nil {
		return e
	}
	e.buf = appendTime(appendKey(e.buf, key), t, TimeFieldFormat)
	return e
}

// Times adds the field key with t formated as string using zerolog.TimeFieldFormat.
func (e *Event) Times(key string, t []time.Time) *Event {
	if e == nil {
		return e
	}
	e.buf = appendTimes(appendKey(e.buf, key), t, TimeFieldFormat)
	return e
}

// Dur adds the field key with duration d stored as zerolog.DurationFieldUnit.
// If zerolog.DurationFieldInteger is true, durations are rendered as integer
// instead of float.
func (e *Event) Dur(key string, d time.Duration) *Event {
	if e == nil {
		return e
	}
	e.buf = appendDuration(appendKey(e.buf, key), d, DurationFieldUnit, DurationFieldInteger)
	return e
}

// Durs adds the field key with duration d stored as zerolog.DurationFieldUnit.
// If zerolog.DurationFieldInteger is true, durations are rendered as integer
// instead of float.
func (e *Event) Durs(key string, d []time.Duration) *Event {
	if e == nil {
		return e
	}
	e.buf = appendDurations(appendKey(e.buf, key), d, DurationFieldUnit, DurationFieldInteger)
	return e
}

// TimeDiff adds the field key with positive duration between time t and start.
// If time t is not greater than start, duration will be 0.
// Duration format follows the same principle as Dur().
func (e *Event) TimeDiff(key string, t time.Time, start time.Time) *Event {
	if e == nil {
		return e
	}
	var d time.Duration
	if t.After(start) {
		d = t.Sub(start)
	}
	e.buf = appendDuration(appendKey(e.buf, key), d, DurationFieldUnit, DurationFieldInteger)
	return e
}

// Interface adds the field key with i marshaled using reflection.
func (e *Event) Interface(key string, i interface{}) *Event {
	if e == nil {
		return e
	}
	if obj, ok := i.(LogObjectMarshaler); ok {
		return e.Object(key, obj)
	}
	e.buf = appendInterface(appendKey(e.buf, key), i)
	return e
}

// Caller adds the file:line of the caller with the zerolog.CallerFieldName key.
func (e *Event) Caller() *Event {
	return e.caller(2)
}

func (e *Event) caller(skip int) *Event {
	if e == nil {
		return e
	}
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return e
	}
	e.buf = appendString(appendKey(e.buf, CallerFieldName), file+":"+strconv.Itoa(line))
	return e
}
