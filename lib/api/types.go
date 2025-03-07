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

package api

import "github.com/vine-io/vine/core/registry"

type Pair struct {
	Key    string   `json:"key,omitempty"`
	Values []string `json:"values,omitempty"`
}

// A HTTP request as RPC
// Forward by the api handler
type Request struct {
	Method string           `json:"method,omitempty"`
	Path   string           `json:"path,omitempty"`
	Header map[string]*Pair `json:"header,omitempty"`
	Get    map[string]*Pair `json:"get,omitempty"`
	Post   map[string]*Pair `json:"post,omitempty"`
	Body   string           `json:"body,omitempty"`
	Url    string           `json:"url,omitempty"`
}

// A HTTP response as RPC
// Expected response for the api handler
type Response struct {
	StatusCode int32            `json:"statusCode,omitempty"`
	Header     map[string]*Pair `json:"header,omitempty"`
	Body       string           `json:"body,omitempty"`
}

// A HTTP event as RPC
// Forwarded by the event handler
type Event struct {
	// e.g login
	Name string `json:"name,omitempty"`
	// uuid
	Id string `json:"id,omitempty"`
	// unix timestamp of event
	Timestamp int64 `json:"timestamp,omitempty"`
	// event headers
	Header map[string]*Pair `json:"header,omitempty"`
	// the event data
	Data string `json:"data,omitempty"`
}

// Endpoint is a mapping between an RPC method and HTTP endpoint
type Endpoint struct {
	// RPC Method e.g. Greeter.Hello
	Name string `json:"name,omitempty"`
	// Description e.g what's this endpoint for
	Description string `json:"description,omitempty"`
	// API Handler e.g rpc, proxy
	Handler string `json:"handler,omitempty"`
	// HTTP Host e.g example.com
	Host []string `json:"host,omitempty"`
	// HTTP Methods e.g GET, POST
	Method []string `json:"method,omitempty"`
	// HTTP Path e.g /greeter. Expect POSIX regex
	Path []string `json:"path,omitempty"`
	// Body destination
	// "*" or "" - top level message value
	// "string" - inner message value
	Body string `json:"body,omitempty"`
	// Stream flag
	Stream bool `json:"stream,omitempty"`
}

// Service represents an API service
type Service struct {
	// Name of service
	Name string `json:"name,omitempty"`
	// The endpoint for this service
	Endpoint *Endpoint `json:"endpoint,omitempty"`
	// Versions of this service
	Services []*registry.Service `json:"services,omitempty"`
}
