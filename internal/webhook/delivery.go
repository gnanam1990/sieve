package webhook

import (
	"container/list"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// deliveryLog is an LRU of recently-seen X-GitHub-Delivery GUIDs, persisted to
// deliveries.jsonl so redeliveries are dropped across restarts. The file is
// rewritten (rotated) to the live LRU snapshot once it has grown by a capacity's
// worth of appends, so it never grows unbounded.
type deliveryLog struct {
	mu    sync.Mutex
	cap   int
	order *list.List // front = most recently seen
	index map[string]*list.Element
	path  string
	f     *os.File
	since int // appends since the last rotation
}

type deliveryRecord struct {
	ID string `json:"id"`
}

func openDeliveryLog(dir string, capacity int) (*deliveryLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("delivery log dir: %w", err)
	}
	path := filepath.Join(dir, "deliveries.jsonl")
	d := &deliveryLog{
		cap:   capacity,
		order: list.New(),
		index: map[string]*list.Element{},
		path:  path,
	}
	if err := d.load(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // daemon data; owner-only
	if err != nil {
		return nil, fmt.Errorf("open delivery log: %w", err)
	}
	d.f = f
	return d, nil
}

// load reads persisted GUIDs oldest-first into the LRU, trimming to capacity.
// A tampered, un-rotated, or oversized file is bounded: if it is larger than
// a reasonable worst-case capacity*line bound, we read only its tail in line-sized
// chunks instead of allocating the whole file via os.ReadFile (an attacker who
// can write to data_dir — the threat model on check 8 — must not be able to OOM
// the daemon on next start with a multi-GB deliveries.jsonl).
func (d *deliveryLog) load() error {
	const maxLine = 256 // a GUID is ~36 bytes; records are tiny
	fi, err := os.Stat(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat delivery log: %w", err)
	}
	var data []byte
	if fi.Size() > int64(d.cap)*maxLine {
		// Large on disk — read only the trailing cap*maxLine bytes; older IDs
		// are already beyond the LRU horizon and would be trimmed anyway.
		f, err := os.Open(d.path) //nolint:gosec // daemon log path
		if err != nil {
			return fmt.Errorf("open delivery log for replay: %w", err)
		}
		buf := make([]byte, int64(d.cap)*maxLine)
		n, _ := f.ReadAt(buf, fi.Size()-int64(len(buf)))
		_ = f.Close()
		data = buf[:n]
	} else {
		data, err = os.ReadFile(d.path) //nolint:gosec // daemon log path; bounded above
		if err != nil {
			return fmt.Errorf("read delivery log: %w", err)
		}
	}
	for _, line := range splitJSONLines(data) {
		var rec deliveryRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.ID == "" {
			continue
		}
		if _, ok := d.index[rec.ID]; ok {
			continue
		}
		d.index[rec.ID] = d.order.PushFront(rec.ID)
	}
	d.trim()
	return nil
}

// has reports whether guid was seen before, refreshing its recency.
func (d *deliveryLog) has(guid string) bool {
	if guid == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.index[guid]; ok {
		d.order.MoveToFront(el)
		return true
	}
	return false
}

// record marks guid seen and persists it, rotating the file when it has grown
// by a capacity's worth of appends.
func (d *deliveryLog) record(guid string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.index[guid]; ok {
		d.order.MoveToFront(el)
		return nil
	}
	d.index[guid] = d.order.PushFront(guid)
	d.trim()

	line, _ := json.Marshal(deliveryRecord{ID: guid})
	if _, err := d.f.Write(append(line, '\n')); err != nil {
		return err
	}
	d.since++
	if d.since >= d.cap {
		return d.rotate()
	}
	return nil
}

// trim evicts the oldest entries beyond capacity.
func (d *deliveryLog) trim() {
	for d.order.Len() > d.cap {
		back := d.order.Back()
		d.order.Remove(back)
		delete(d.index, back.Value.(string))
	}
}

// rotate rewrites the file to the live LRU snapshot (oldest-first) so it stays
// bounded. Caller holds d.mu.
func (d *deliveryLog) rotate() error {
	tmp := d.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // daemon data; owner-only
	if err != nil {
		return err
	}
	for el := d.order.Back(); el != nil; el = el.Prev() { // oldest → newest
		line, _ := json.Marshal(deliveryRecord{ID: el.Value.(string)})
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close() //nolint:errcheck
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := d.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, d.path); err != nil {
		return err
	}
	nf, err := os.OpenFile(d.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // daemon data; owner-only
	if err != nil {
		return err
	}
	d.f = nf
	d.since = 0
	return nil
}

func (d *deliveryLog) close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return nil
	}
	return d.f.Close()
}

func splitJSONLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
