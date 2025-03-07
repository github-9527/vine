// MIT License
//
// Copyright (c) 2020 Lack
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

package mdns

import (
	"os"
	"testing"
	"time"

	"github.com/vine-io/vine/core/registry"
)

func TestMDNS(t *testing.T) {
	// skip test in travis because of sendto: operation not permitted error
	if travis := os.Getenv("TRAVIS"); travis == "true" {
		t.Skip()
	}

	testData := []*registry.Service{
		{
			Name:    "test1",
			Version: "1.0.1",
			Nodes: []*registry.Node{
				{
					Id:      "test1-1",
					Address: "10.0.0.1:10001",
					Metadata: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
		{
			Name:    "test2",
			Version: "1.0.2",
			Nodes: []*registry.Node{
				{
					Id:      "test2-1",
					Address: "10.0.0.2:10002",
					Metadata: map[string]string{
						"foo2": "bar2",
					},
				},
			},
		},
		{
			Name:    "test3",
			Version: "1.0.3",
			Nodes: []*registry.Node{
				{
					Id:      "test3-1",
					Address: "10.0.0.3:10003",
					Metadata: map[string]string{
						"foo3": "bar3",
					},
				},
			},
		},
	}

	travis := os.Getenv("TRAVIS")

	var opts []registry.Option

	if travis == "true" {
		opts = append(opts, registry.Timeout(time.Millisecond*100))
	}

	// new registry
	r := NewRegistry(opts...)

	for _, service := range testData {
		// register service
		if err := r.Register(service); err != nil {
			t.Fatal(err)
		}

		// get registered service
		s, err := r.GetService(service.Name)
		if err != nil {
			t.Fatal(err)
		}

		if len(s) != 1 {
			t.Fatalf("Expected one result for %s got %d", service.Name, len(s))
		}

		if s[0].Name != service.Name {
			t.Fatalf("Expected name %s got %s", service.Name, s[0].Name)
		}

		if s[0].Version != service.Version {
			t.Fatalf("Expected version %s got %s", service.Version, s[0].Version)
		}

		if len(s[0].Nodes) != 1 {
			t.Fatalf("Expected 1 node, got %d", len(s[0].Nodes))
		}

		node := s[0].Nodes[0]

		if node.Id != service.Nodes[0].Id {
			t.Fatalf("Expected node id %s got %s", service.Nodes[0].Id, node.Id)
		}

		if node.Address != service.Nodes[0].Address {
			t.Fatalf("Expected node address %s got %s", service.Nodes[0].Address, node.Address)
		}
	}

	services, err := r.ListServices()
	if err != nil {
		t.Fatal(err)
	}

	for _, service := range testData {
		var seen bool
		for _, s := range services {
			if s.Name == service.Name {
				seen = true
				break
			}
		}
		if !seen {
			t.Fatalf("Expected service %s got nothing", service.Name)
		}

		// deregister
		if err := r.Deregister(service); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Millisecond * 5)

		// check its gone
		s, _ := r.GetService(service.Name)
		if len(s) > 0 {
			t.Fatalf("Expected nothing got %+v", s[0])
		}
	}

}

func TestEncoding(t *testing.T) {
	testData := []*mdnsTxt{
		{
			Version: "1.0.0",
			Metadata: map[string]string{
				"foo": "bar",
			},
			Endpoints: []*registry.Endpoint{
				{
					Name: "endpoint1",
					Request: &registry.Value{
						Name: "request",
						Type: "request",
					},
					Response: &registry.Value{
						Name: "response",
						Type: "response",
					},
					Metadata: map[string]string{
						"foo1": "bar1",
					},
				},
			},
		},
	}

	for _, d := range testData {
		encoded, err := encode(d)
		if err != nil {
			t.Fatal(err)
		}

		for _, txt := range encoded {
			if len(txt) > 255 {
				t.Fatalf("One of parts for txt is %d characters", len(txt))
			}
		}

		decoded, err := decode(encoded)
		if err != nil {
			t.Fatal(err)
		}

		if decoded.Version != d.Version {
			t.Fatalf("Expected version %s got %s", d.Version, decoded.Version)
		}

		if len(decoded.Endpoints) != len(d.Endpoints) {
			t.Fatalf("Expected %d endpoints, got %d", len(d.Endpoints), len(decoded.Endpoints))
		}

		for k, v := range d.Metadata {
			if val := decoded.Metadata[k]; val != v {
				t.Fatalf("Expected %s=%s got %s=%s", k, v, k, val)
			}
		}
	}

}

func TestWatcher(t *testing.T) {
	if travis := os.Getenv("TRAVIS"); travis == "true" {
		t.Skip()
	}

	testData := []*registry.Service{
		{
			Name:    "test1",
			Version: "1.0.1",
			Nodes: []*registry.Node{
				{
					Id:      "test1-1",
					Address: "10.0.0.1:10001",
					Metadata: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
		{
			Name:    "test2",
			Version: "1.0.2",
			Nodes: []*registry.Node{
				{
					Id:      "test2-1",
					Address: "10.0.0.2:10002",
					Metadata: map[string]string{
						"foo2": "bar2",
					},
				},
			},
		},
		{
			Name:    "test3",
			Version: "1.0.3",
			Nodes: []*registry.Node{
				{
					Id:      "test3-1",
					Address: "10.0.0.3:10003",
					Metadata: map[string]string{
						"foo3": "bar3",
					},
				},
			},
		},
	}

	testFn := func(service, s *registry.Service) {
		if s == nil {
			t.Fatalf("Expected one result for %s got nil", service.Name)

		}

		if s.Name != service.Name {
			t.Fatalf("Expected name %s got %s", service.Name, s.Name)
		}

		if s.Version != service.Version {
			t.Fatalf("Expected version %s got %s", service.Version, s.Version)
		}

		if len(s.Nodes) != 1 {
			t.Fatalf("Expected 1 node, got %d", len(s.Nodes))
		}

		node := s.Nodes[0]

		if node.Id != service.Nodes[0].Id {
			t.Fatalf("Expected node id %s got %s", service.Nodes[0].Id, node.Id)
		}

		if node.Address != service.Nodes[0].Address {
			t.Fatalf("Expected node address %s got %s", service.Nodes[0].Address, node.Address)
		}
	}

	travis := os.Getenv("TRAVIS")

	var opts []registry.Option

	if travis == "true" {
		opts = append(opts, registry.Timeout(time.Millisecond*100))
	}

	// new registry
	r := NewRegistry(opts...)

	w, err := r.Watch()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	for _, service := range testData {
		// register service
		if err := r.Register(service); err != nil {
			t.Fatal(err)
		}

		for {
			res, err := w.Next()
			if err != nil {
				t.Fatal(err)
			}

			if res.Service.Name != service.Name {
				continue
			}

			if res.Action != "create" {
				t.Fatalf("Expected create event got %s for %s", res.Action, res.Service.Name)
			}

			testFn(service, res.Service)
			break
		}

		// deregister
		if err := r.Deregister(service); err != nil {
			t.Fatal(err)
		}

		for {
			res, err := w.Next()
			if err != nil {
				t.Fatal(err)
			}

			if res.Service.Name != service.Name {
				continue
			}

			if res.Action != "delete" {
				continue
			}

			testFn(service, res.Service)
			break
		}
	}
}
