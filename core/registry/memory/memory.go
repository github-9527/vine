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

// Package memory provides an in-memory registry
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vine-io/vine/core/registry"
	"github.com/vine-io/vine/lib/logger"
)

var (
	sendEventTime = 10 * time.Millisecond
	ttlPruneTime  = time.Second
)

type node struct {
	*registry.Node
	TTL      time.Duration
	LastSeen time.Time
}

type record struct {
	Name      string
	Version   string
	Metadata  map[string]string
	Nodes     map[string]*node
	Endpoints []*registry.Endpoint
	Apis      []*registry.OpenAPI
}

type Registry struct {
	options registry.Options

	sync.RWMutex
	records  map[string]map[string]*record
	watchers map[string]*Watcher
}

func NewRegistry(opts ...registry.Option) registry.Registry {
	options := registry.Options{
		Context: context.Background(),
	}

	for _, o := range opts {
		o(&options)
	}

	records := getServiceRecords(options.Context)
	if records == nil {
		records = make(map[string]map[string]*record)
	}

	reg := &Registry{
		options:  options,
		records:  records,
		watchers: make(map[string]*Watcher),
	}

	go reg.ttlPrune()

	return reg
}

func (m *Registry) ttlPrune() {
	prune := time.NewTicker(ttlPruneTime)
	defer prune.Stop()

	for {
		select {
		case <-prune.C:
			m.Lock()
			for name, records := range m.records {
				for version, record := range records {
					for id, n := range record.Nodes {
						if n.TTL != 0 && time.Since(n.LastSeen) > n.TTL {
							logger.Debugf("Registry TTL expired for node %s of service %s", n.Id, name)
						}
						delete(m.records[name][version].Nodes, id)
					}
				}
			}
			m.Unlock()
		}
	}
}

func (m *Registry) sendEvent(r *registry.Result) {
	m.RLock()
	watchers := make([]*Watcher, 0, len(m.watchers))
	for _, w := range m.watchers {
		watchers = append(watchers, w)
	}
	m.RUnlock()

	for _, w := range watchers {
		select {
		case <-w.exit:
			m.Lock()
			delete(m.watchers, w.id)
			m.Unlock()
		default:
			select {
			case w.res <- r:
			case <-time.After(sendEventTime):
			}
		}
	}
}

func (m *Registry) Init(opts ...registry.Option) error {
	for _, o := range opts {
		o(&m.options)
	}

	// add services
	m.Lock()
	defer m.Unlock()

	records := getServiceRecords(m.options.Context)
	for name, record := range records {
		// add a whole new service including all of its versions
		if _, ok := m.records[name]; !ok {
			m.records[name] = record
			continue
		}
		// add the versions of the service we don't track yet
		for version, r := range record {
			if _, ok := m.records[name][version]; !ok {
				m.records[name][version] = r
				continue
			}
		}
	}

	return nil
}

func (m *Registry) Options() registry.Options {
	return m.options
}

func (m *Registry) Register(s *registry.Service, opts ...registry.RegisterOption) error {
	m.Lock()
	defer m.Unlock()

	var options registry.RegisterOptions
	for _, o := range opts {
		o(&options)
	}

	r := serviceToRecord(s, options.TTL)

	if _, ok := m.records[s.Name]; !ok {
		m.records[s.Name] = make(map[string]*record)
	}

	if _, ok := m.records[s.Name][s.Version]; !ok {
		m.records[s.Name][s.Version] = r
		logger.Debugf("Registry added new service: %s, version: %s", s.Name, s.Version)
		go m.sendEvent(&registry.Result{Action: "update", Service: s})
		return nil
	}

	addedNodes := false
	for _, n := range s.Nodes {
		if _, ok := m.records[s.Name][s.Version].Nodes[n.Id]; !ok {
			addedNodes = true
			metadata := make(map[string]string)
			for k, v := range n.Metadata {
				metadata[k] = v
				m.records[s.Name][s.Version].Nodes[n.Id] = &node{
					Node: &registry.Node{
						Id:       n.Id,
						Address:  n.Address,
						Metadata: metadata,
					},
					TTL:      options.TTL,
					LastSeen: time.Now(),
				}
			}
		}
	}

	if addedNodes {
		logger.Debugf("Registry added new node to service: %s, version: %s", s.Name, s.Version)
		go m.sendEvent(&registry.Result{Action: "update", Service: s})
		return nil
	}

	// refresh TTL and timestamp
	for _, n := range s.Nodes {
		logger.Debugf("Updated registration for service: %s, version: %s", s.Name, s.Version)
		m.records[s.Name][s.Version].Nodes[n.Id].TTL = options.TTL
		m.records[s.Name][s.Version].Nodes[n.Id].LastSeen = time.Now()
	}

	return nil
}

func (m *Registry) Deregister(s *registry.Service, opts ...registry.DeregisterOption) error {
	m.Lock()
	defer m.Unlock()

	if _, ok := m.records[s.Name]; ok {
		if _, ok := m.records[s.Name][s.Version]; ok {
			for _, n := range s.Nodes {
				if _, ok := m.records[s.Name][s.Version].Nodes[n.Id]; ok {
					logger.Debugf("Registry removed node from service: %s, version: %s", s.Name, s.Version)
					delete(m.records[s.Name][s.Version].Nodes, n.Id)
				}
			}
			if len(m.records[s.Name][s.Version].Nodes) == 0 {
				delete(m.records[s.Name], s.Version)
				logger.Debugf("Registry removed service: %s, version: %s", s.Name, s.Version)
			}
		}
		if len(m.records[s.Name]) == 0 {
			delete(m.records, s.Name)
			logger.Debugf("Registry removed service: %s", s.Name)
		}
		go m.sendEvent(&registry.Result{Action: "delete", Service: s})
	}

	return nil
}

func (m *Registry) GetService(name string, opts ...registry.GetOption) ([]*registry.Service, error) {
	m.RLock()
	defer m.RUnlock()

	records, ok := m.records[name]
	if !ok {
		return nil, registry.ErrNotFound
	}

	services := make([]*registry.Service, len(m.records[name]))
	i := 0
	for _, record := range records {
		services[i] = recordToService(record)
		i++
	}

	return services, nil
}

func (m *Registry) ListServices(opts ...registry.ListOption) ([]*registry.Service, error) {
	m.RLock()
	defer m.RUnlock()

	var services []*registry.Service
	for _, records := range m.records {
		for _, record := range records {
			services = append(services, recordToService(record))
		}
	}

	return services, nil
}

func (m *Registry) Watch(opts ...registry.WatchOption) (registry.Watcher, error) {
	var wo registry.WatchOptions
	for _, o := range opts {
		o(&wo)
	}

	w := &Watcher{
		exit: make(chan bool),
		res:  make(chan *registry.Result),
		id:   uuid.New().String(),
		wo:   wo,
	}

	m.Lock()
	m.watchers[w.id] = w
	m.Unlock()

	return w, nil
}

func (m *Registry) String() string {
	return "memory"
}
