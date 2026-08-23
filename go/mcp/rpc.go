// Package mcp lets a model work with the mailbox: search, read, file, and write
// a draft. It speaks the Model Context Protocol over stdin and stdout.
//
// The protocol is JSON-RPC 2.0, one message per line. That is little enough to
// write out here, and writing it out keeps the dependency list at "standard
// library plus AWS SDK plus go-message" - the same call as tools/zip.go, where
// the zipper was written rather than imported.
//
// It sits next to web/ and not below it: core/ and store/ know nothing of HTTP
// or of an interface, which is what makes a second front end cheap.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// version is the protocol revision this server was written against. A client
// that asks for another gets its own back when we know it - the shape of what
// we send does not differ between these revisions.
const version = "2025-06-18"

var known = map[string]bool{"2024-11-05": true, "2025-03-26": true, version: true}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse       = -32700
	codeInvalid     = -32600
	codeNoSuchThing = -32601
	codeBadParams   = -32602
	codeInternal    = -32603
)

// Serve reads requests until the input ends.
//
// A notification - a message without an id - gets no answer, which the protocol
// requires: answering one makes clients that follow the spec close the
// connection.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	// A message with a big attachment in it is longer than the default 64 KB.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var mu sync.Mutex
	write := func(r response) {
		blob, err := json.Marshal(r)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(out, "%s\n", blob)
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(response{JSONRPC: "2.0", Error: &rpcError{codeParse, "cannot read that as JSON"}})
			continue
		}
		if req.JSONRPC != "" && req.JSONRPC != "2.0" {
			write(response{JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{codeInvalid, "this speaks JSON-RPC 2.0"}})
			continue
		}
		result, rerr := s.handle(req)
		if req.ID == nil {
			continue // a notification wants no answer
		}
		write(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
	}
	return sc.Err()
}
