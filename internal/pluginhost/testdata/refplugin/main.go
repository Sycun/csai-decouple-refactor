// Command refplugin is the reference plugin the host tests drive. It speaks the
// csai-plugin/1 ABI and deliberately exercises the security-relevant paths:
// environment visibility, grant checks outside its ceiling, egress through the
// injected proxy, an out-of-ABI callback, and self-termination mid-call.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const protocol = "csai-plugin/1"

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   map[string]any  `json:"error,omitempty"`
}

type responder struct {
	out  *bufio.Writer
	next int64
}

func (r *responder) reply(id int64, result any) {
	data, _ := json.Marshal(result)
	ident := id
	r.send(&frame{JSONRPC: "2.0", ID: &ident, Result: data})
}

func (r *responder) replyErr(id int64, code int, message string) {
	ident := id
	r.send(&frame{JSONRPC: "2.0", ID: &ident, Error: map[string]any{"code": code, "message": message}})
}

func (r *responder) request(method string, params any) {
	data, _ := json.Marshal(params)
	r.next++
	r.send(&frame{JSONRPC: "2.0", ID: &r.next, Method: method, Params: data})
}

func (r *responder) send(f *frame) {
	data, _ := json.Marshal(f)
	r.out.Write(data)
	r.out.WriteByte('\n')
	r.out.Flush()
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	responder := &responder{out: out}

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}
		var request frame
		if err := json.Unmarshal(line, &request); err != nil {
			continue
		}

		if request.Method == "" && request.ID != nil {
			// A reply to one of our callbacks: echo it on stderr so the test can read
			// the host's decision out of the invoke result instead.
			fmt.Fprintf(os.Stderr, "callback_reply %s\n", string(request.Result))
			continue
		}

		switch request.Method {
		case "initialize":
			if request.ID == nil {
				continue
			}
			reported := protocol
			if os.Getenv("REF_BAD_PROTOCOL") == "1" {
				reported = "csai-plugin/0"
			}
			responder.reply(*request.ID, map[string]any{
				"protocol":     reported,
				"name":         "refplugin",
				"version":      "1.0.0",
				"capabilities": []string{"ref.echo", "ref.env", "ref.grant", "ref.egress", "ref.crash", "ref.badcallback"},
			})
		case "capabilities/list":
			if request.ID == nil {
				continue
			}
			// Two ways to answer badly, for the host's two rejection rules: the ABI shape and the
			// publisher namespace. The default answer stays as it was, so every existing test is
			// unaffected.
			switch os.Getenv("REF_BAD_CAPS") {
			case "objects":
				responder.reply(*request.ID, []map[string]any{{"id": "ref.echo"}})
				continue
			case "foreign":
				responder.reply(*request.ID, []string{"ref.echo", "vendor2.tool"})
				continue
			case "blank":
				responder.reply(*request.ID, []string{"ref.echo", "  "})
				continue
			}
			responder.reply(*request.ID, []string{"ref.echo", "ref.env", "ref.grant", "ref.egress", "ref.crash", "ref.badcallback"})
		case "capabilities/invoke":
			if request.ID == nil {
				continue
			}
			responder.handle(request.ID, request.Params)
		case "shutdown":
			if request.ID != nil {
				responder.reply(*request.ID, map[string]bool{"ok": true})
			}
			return
		default:
			if request.ID != nil {
				responder.replyErr(*request.ID, -32601, "unknown method")
			}
		}
	}
}

type invokeParams struct {
	CapabilityID string          `json:"capabilityId"`
	Params       json.RawMessage `json:"params"`
}

type invokeArgs struct {
	Text   string `json:"text"`
	Grant  string `json:"grant"`
	URL    string `json:"url"`
	Method string `json:"method"`
}

func (r *responder) handle(id *int64, raw json.RawMessage) {
	var params invokeParams
	_ = json.Unmarshal(raw, &params)
	args := invokeArgs{}
	if len(params.Params) > 0 {
		_ = json.Unmarshal(params.Params, &args)
	}

	switch params.CapabilityID {
	case "ref.echo":
		r.reply(*id, map[string]any{"content": args.Text})
	case "ref.env":
		env := map[string]string{}
		for _, entry := range os.Environ() {
			if key, value, ok := strings.Cut(entry, "="); ok {
				env[key] = value
			}
		}
		payload, _ := json.Marshal(env)
		r.reply(*id, map[string]any{"content": string(payload)})
	case "ref.grant":
		r.request("host/grant_check", map[string]string{"grant": args.Grant})
		// The answer arrives as a separate frame; give the reader a turn and then
		// report what the host said via stderr.
		time.Sleep(250 * time.Millisecond)
		r.reply(*id, map[string]any{"content": "grant_check_sent", "isError": false})
	case "ref.egress":
		method := args.Method
		if method == "" {
			method = "GET"
		}
		request, err := http.NewRequest(method, args.URL, nil)
		if err != nil {
			r.reply(*id, map[string]any{"content": err.Error(), "isError": true})
			return
		}
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			r.reply(*id, map[string]any{"content": err.Error(), "isError": true})
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		r.reply(*id, map[string]any{"content": fmt.Sprintf("%d %s", response.StatusCode, strings.TrimSpace(string(body)))})
	case "ref.badcallback":
		r.request("host/secrets_dump", map[string]string{"why": "test"})
		time.Sleep(250 * time.Millisecond)
		r.reply(*id, map[string]any{"content": "badcallback_sent"})
	case "ref.crash":
		os.Exit(9)
	default:
		r.replyErr(*id, -32001, "capability is not implemented by the reference plugin")
	}
}
