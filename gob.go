/*
Package gob provides a gob codec for Gorilla RPC over HTTP.

Why Gob

At time of writing, the Gorilla project only offers one codec for its
RPC over HTTP package, namely JSON. JSON is nice and all, but for Go
programs talking to Go programs, gob is superior because it takes
advantage of the common type system, not to mention the simple truth
that a binary format takes up less space on the wire.

The primary use case for this package is web applications consisting
of a Go server on the backend and GopherJS on the frontend. If those
two components are in place, then there's no real reason to choose JSON
over gob. It's even possible to have both enabled simultaneously,
thanks to Gorilla RPC's use of the Content-Type header to specify
codec.

Note that net/rpc over Websockets is another way to enable gob-RPC,
but due to the way that net/rpc service methods are defined, it's
impossible to get any context about the client during an RPC method
call unless it's provided explicitly each time. Gorilla RPC signatures
add an *http.Request parameter that can be examined to get this type
of information.
*/
package gob

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"reflect"
	"runtime"

	"github.com/gorilla/rpc/v2"
)

// NewCodec returns a new gob codec to register with a Gorilla RPC server.
func NewCodec() *Codec {
	return &Codec{}
}

type Codec struct {
}

func (c *Codec) NewRequest(r *http.Request) rpc.CodecRequest {
	req := new(rpcRequest)
	err := gob.NewDecoder(r.Body).Decode(req)
	r.Body.Close()
	return &CodecRequest{request: req, err: err}
}

type CodecRequest struct {
	request *rpcRequest
	err     error
}

func (c *CodecRequest) Method() (string, error) {
	if c.err == nil {
		return c.request.Method, nil
	}
	return "", c.err
}

func (c *CodecRequest) ReadRequest(args interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); ok {
				panic(r)
			}
			switch t := r.(type) {
			case error:
				err = t
			case string:
				err = NewError(t)
			default:
				err = fmt.Errorf("%v", t)
			}
		}
	}()

	if c.err == nil && c.request.Params != nil {
		var (
			va = reflect.ValueOf(args).Elem()
			vb = reflect.ValueOf(c.request.Params)
		)
		if !vb.Type().AssignableTo(va.Type()) {
			return NewError(fmt.Sprintf("invalid parameter: expected %s, but got %s", va.Type().Name(), vb.Type().Name()))
		}
		va.Set(vb)
	}

	return c.err
}

func (c *CodecRequest) WriteResponse(w http.ResponseWriter, reply interface{}) {
	// A request id of 0 is a notification and needs no response.
	if c.request.Id != 0 {
		c.writeServerResponse(w, http.StatusOK, &rpcResponse{
			Result: reply,
			Error:  nil,
			Id:     c.request.Id,
		})
	}
}

func (c *CodecRequest) WriteError(w http.ResponseWriter, _ int, err error) {
	c.writeServerResponse(w, http.StatusBadRequest, &rpcResponse{
		Result: nil,
		Error:  err,
		Id:     c.request.Id,
	})
}

func (c *CodecRequest) writeServerResponse(w http.ResponseWriter, status int, res *rpcResponse) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(res); err != nil {
		w.WriteHeader(http.StatusInternalServerError)

		var hint string
		if err.Error() == "gob: type not registered for interface: errors.errorString" {
			hint = " (hint: use gob.NewError() instead)"
		}

		// The result couldn't be encoded, so send a value that we know
		// will succeed so that the client knows what happened.
		gob.NewEncoder(w).Encode(&rpcResponse{
			Result: nil,
			Error:  NewError(err.Error() + hint),
			Id:     res.Id,
		})
		return
	}

	w.WriteHeader(status)
	w.Header().Set("Content-Type", "application/gob; charset=binary")
	io.Copy(w, &buf)
}

// EncodeClientRequest encodes parameters for a gob-RPC client request.
func EncodeClientRequest(method string, args interface{}) ([]byte, error) {
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(&rpcRequest{
		Method: method,
		Params: args,
		Id:     uint64(rand.Int63()) + 1, // ensure a non-zero id
	})
	return buf.Bytes(), err
}

// BuildRequest builds an HTTP request for calling a gob-RPC method.
//
// The body of the request is created using EncodeClientRequest(), the
// verb is set to POST, and the Content-Type header is set to
// "application/gob; charset=binary".
func BuildRequest(url, method string, args interface{}) (*http.Request, error) {
	message, err := EncodeClientRequest(method, args)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(message))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/gob; charset=binary")
	return req, nil
}

// DecodeClientResponse decodes the response body of a client request into the interface reply.
func DecodeClientResponse(r io.Reader, reply interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); ok {
				panic(r)
			}
			switch t := r.(type) {
			case error:
				err = t
			case string:
				err = NewError(t)
			default:
				err = fmt.Errorf("%v", t)
			}
		}
	}()

	var res rpcResponse
	if err := gob.NewDecoder(r).Decode(&res); err != nil {
		return err
	}

	if res.Error != nil {
		return res.Error
	}

	var (
		va = reflect.ValueOf(reply).Elem()
		vb = reflect.ValueOf(res.Result)
	)
	if !vb.Type().AssignableTo(va.Type()) {
		return NewError(fmt.Sprintf("invalid return value: method returns %s, not %s", vb.Type().Name(), va.Type().Name()))
	}

	va.Set(vb)
	return nil
}

func init() {
	gob.Register(&rpcRequest{})
	gob.Register(&errorString{})
}

type rpcRequest struct {
	Method string
	Params interface{}
	Id     uint64
}

type rpcResponse struct {
	Result interface{}
	Error  error
	Id     uint64
}

type errorString struct {
	S string
}

func (e *errorString) Error() string {
	return e.S
}

// NewError returns a gob-registered error that formats as the given text.
//
// Unfortunately, errors created by the standard library's errors package
// are not registered with encoding/gob, which is necessary in order to send
// it over the wire via gob, and since the struct is private to the package,
// there's no way for us to do it for them. As a result, returning an error
// using errors.New(...) from an RPC method, when using this encoding, will
// cause the client to receive an EOF error
func NewError(text string) error {
	return &errorString{text}
}
