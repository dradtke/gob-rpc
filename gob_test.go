package gob

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/rpc/v2"
)

var (
	rs *rpc.Server
	ts *httptest.Server
)

type SomeService struct{}

func (s *SomeService) Echo(_ *http.Request, args *string, reply *string) error {
	*reply = *args
	return nil
}

func (s *SomeService) Error(*http.Request, *struct{}, *struct{}) error {
	return NewError("uh-oh")
}

func TestEcho(t *testing.T) {
	var reply string
	if err := doRequest("SomeService.Echo", "hello", &reply); err != nil {
		t.Fatal(err)
	}
	if reply != "hello" {
		t.Errorf("received unexpected response: %s", reply)
	}
}

func TestBadParameter(t *testing.T) {
	var result string
	err := doRequest("SomeService.Echo", 3, &result)
	if err == nil {
		t.Fatal("expected an error, but none was returned")
	}
	if !strings.Contains(err.Error(), "invalid parameter") {
		t.Fatalf("received unexpected error: %s", err)
	}
}

func TestBadReturn(t *testing.T) {
	var result int
	err := doRequest("SomeService.Echo", "hello", &result)
	if err == nil {
		t.Fatal("expected an error, but none was returned")
	}
	if !strings.Contains(err.Error(), "invalid return value") {
		t.Fatalf("received unexpected error: %s", err)
	}
}

func TestError(t *testing.T) {
	err := doRequest("SomeService.Error", nil, nil)
	if err == nil {
		t.Fatal("expected an error, but none was returned")
	}
	if err.Error() != "uh-oh" {
		t.Fatalf("received unexpected error: %s", err)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()

	rs = rpc.NewServer()
	rs.RegisterCodec(NewCodec(), "application/gob")
	rs.RegisterService(&SomeService{}, "")
	ts = httptest.NewServer(rs)

	exitCode := m.Run()

	ts.Close()
	os.Exit(exitCode)
}

func doRequest(method string, args, reply interface{}) error {
	req, err := BuildRequest(ts.URL, method, args)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	err = DecodeClientResponse(resp.Body, reply)
	if err != nil {
		return err
	}

	return nil
}
