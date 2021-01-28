// Copyright 2011 Google Inc. All Rights Reserved.
// This file is available under the Apache license.

package metrics

import (
	"context"
	"encoding/json"
	"hash/maphash"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"
)

// Store contains Metrics.
type Store struct {
	Metrics  sync.Map
	hashSeed maphash.Seed
}

// NewStore returns a new metric Store.
func NewStore() (s *Store) {
	s = &Store{hashSeed: maphash.MakeSeed()}
	s.ClearMetrics()
	return
}

func (s *Store) hashMetric(name, prog string) uint64 {
	var h maphash.Hash
	h.SetSeed(s.hashSeed)
	h.WriteString(name)
	h.WriteString(prog)
	return h.Sum64()
}

// Add is used to add one metric to the Store.
func (s *Store) Add(m *Metric) error {
	m.RLock()
	k := s.hashMetric(m.Name, m.Program)
	m.RUnlock()
	actual, loaded := s.Metrics.LoadOrStore(k, m)
	if !loaded {
		return nil
	}
	v := actual.(*Metric)
	if m.Kind != v.Kind {
		return errors.Errorf("Metric %s has different kind %v to existing %v.", m.Name, m.Kind, v)
	}

	glog.V(2).Infof("v keys: %v m.keys: %v", v.Keys, m.Keys)

	// If a set of label keys has changed, discard
	// old metric completely, w/o even copying old
	// data, as they are now incompatible.
	if len(v.Keys) == len(m.Keys) && reflect.DeepEqual(v.Keys, m.Keys) {

		glog.V(2).Infof("v buckets: %v m.buckets: %v", v.Buckets, m.Buckets)

		// Otherwise, copy everything into the new metric
		for j, oldLabel := range v.LabelValues {
			glog.V(2).Infof("Labels: %d %s", j, oldLabel.Labels)
			d, err := v.GetDatum(oldLabel.Labels...)
			if err == nil {
				if err = m.RemoveDatum(oldLabel.Labels...); err == nil {
					m.LabelValues = append(m.LabelValues, &LabelValue{Labels: oldLabel.Labels, Value: d})
				}
			}
		}
	}

	s.Metrics.Store(k, m)
	return nil
}

// FindMetricOrNil returns a metric in a store, or returns nil if not found.
func (s *Store) FindMetricOrNil(name, prog string) *Metric {
	k := s.hashMetric(name, prog)
	m, ok := s.Metrics.Load(k)
	if ok {
		return m.(*Metric)
	}
	return nil
}

// ClearMetrics empties the store of all metrics.
func (s *Store) ClearMetrics() {
	s.Metrics.Range(func(key, value interface{}) bool {
		s.Metrics.Delete(key)
		return true
	})
}

// MarshalJSON returns a JSON byte string representing the Store.
func (s *Store) MarshalJSON() (b []byte, err error) {
	ms := make([]*Metric, 0)
	s.Metrics.Range(func(key, value interface{}) bool {
		m := value.(*Metric)
		ms = append(ms, m)
		return true
	})
	return json.Marshal(ms)
}

// Range calls f sequentially for each Metric present in the store.
// The Metric is not locked when f is called.
// If f returns non nil error, Range stops the iteration.
// This looks a lot like sync.Map, ay.
func (s *Store) Range(f func(*Metric) error) (r error) {
	s.Metrics.Range(func(key, value interface{}) bool {
		if err := f(value.(*Metric)); err != nil {
			r = err
			return false
		}
		return true
	})
	return
}

// Gc iterates through the Store looking for metrics that have been marked
// for expiry, and removing them if their expiration time has passed.
func (s *Store) Gc() error {
	glog.Info("Running Store.Expire()")
	now := time.Now()
	return s.Range(func(m *Metric) error {
		for _, lv := range m.LabelValues {
			if lv.Expiry <= 0 {
				continue
			}
			if now.Sub(lv.Value.TimeUTC()) > lv.Expiry {
				err := m.RemoveDatum(lv.Labels...)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// StartGcLoop runs a permanent goroutine to expire metrics every duration.
func (s *Store) StartGcLoop(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		glog.Infof("Metric store expiration disabled")
		return
	}
	go func() {
		glog.Infof("Starting metric store expiry loop every %s", duration.String())
		ticker := time.NewTicker(duration)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.Gc(); err != nil {
					glog.Info(err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// WriteMetrics dumps the current state of the metrics store in JSON format to
// the io.Writer.
func (s *Store) WriteMetrics(w io.Writer) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal metrics into json")
	}
	_, err = w.Write(b)
	if err != nil {
		return errors.Wrap(err, "failed to write metrics")
	}
	return nil
}
