package client

import (
	"os"
	"testing"
)

// go test example:
// `LAINLET_ADDR=192.168.77.21:9001 go test -v`

func TestGet(t *testing.T) {
	addr := os.Getenv("LAINLET_ADDR")
	if resp, err := Get(addr, "hello"); err != nil {
		t.Error(err)
	} else {
		t.Log(string(resp))
	}
}
