package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sadatdaniel/download_me_not/internal/engine"
	"github.com/sadatdaniel/download_me_not/internal/store"
)

// torrentOf returns the running torrent of a job.
func (m *Manager) torrentOf(id string) (engine.Torrent, error) {
	if _, err := m.get(id); err != nil {
		return nil, err
	}
	m.mu.Lock()
	r := m.run[id]
	m.mu.Unlock()
	if r == nil {
		return nil, ErrNotRunning
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tor == nil {
		return nil, ErrNotRunning
	}
	return r.tor, nil
}

func (m *Manager) Peers(id string) ([]engine.Peer, error) {
	t, err := m.torrentOf(id)
	if err != nil {
		return nil, err
	}
	return t.Peers(), nil
}

type PieceView struct {
	engine.Swarm
	Runs []engine.PieceRun `json:"runs"`
	Hint string            `json:"hint"`
}

func (m *Manager) Pieces(id string) (PieceView, error) {
	t, err := m.torrentOf(id)
	if err != nil {
		return PieceView{}, err
	}
	sw := t.Swarm()
	return PieceView{Swarm: sw, Runs: t.Pieces(), Hint: m.hint(sw, t.Stats())}, nil
}

// hint explains in plain words why a torrent is slow or stalled (scope §5.9).
func (m *Manager) hint(sw engine.Swarm, st engine.Stats) string {
	switch {
	case sw.Pieces == 0:
		return "Fetching metadata: waiting for a peer to send the torrent's file list."
	case st.Total > 0 && st.Completed >= st.Total:
		return "Complete."
	case sw.Peers == 0:
		return "No peers connected. The trackers may be down or the torrent may have no seeders; with DHT off, trackers are the only source of peers."
	case sw.Seeders == 0 && sw.Availability < 1:
		return fmt.Sprintf("No complete copy among connected peers (availability %.2f). It can only finish if a seeder appears.", sw.Availability)
	}
	if m.o.Limiter != nil && float64(m.o.Limiter.Limit()) > 0 && st.DownRate >= 0.9*float64(m.o.Limiter.Limit()) {
		return "Limited by your download speed limit."
	}
	if st.DownRate < 10<<10 {
		return fmt.Sprintf("Connected to %d peers (%d seeders), but they are sending little data. They may be busy or choking us; an open incoming port usually helps.", sw.Peers, sw.Seeders)
	}
	return ""
}

func (m *Manager) Trackers(id string) ([][]string, error) {
	t, err := m.torrentOf(id)
	if err != nil {
		return nil, err
	}
	return t.Trackers(), nil
}

// SetTrackers replaces the tracker list now and remembers it for restarts.
func (m *Manager) SetTrackers(id string, tiers [][]string) error {
	t, err := m.torrentOf(id)
	if err != nil {
		return err
	}
	t.SetTrackers(tiers)
	j, err := m.get(id)
	if err != nil {
		return err
	}
	var meta torMeta
	json.Unmarshal(j.Meta, &meta)
	meta.Trackers = tiers
	b, _ := json.Marshal(meta)
	return store.SetJob(m.o.DB, id, map[string]any{"meta": json.RawMessage(b)})
}

// Recheck verifies the data on disk in the background.
func (m *Manager) Recheck(id string) error {
	t, err := m.torrentOf(id)
	if err != nil {
		return err
	}
	store.SetJob(m.o.DB, id, map[string]any{"state": "checking"})
	go func() {
		err := t.Recheck(context.Background())
		f := map[string]any{"state": "downloading"}
		if err != nil {
			f["error"] = "recheck: " + err.Error()
		}
		store.SetJob(m.o.DB, id, f)
	}()
	return nil
}

// Files lists a running torrent's files (the file picker after metadata arrives).
func (m *Manager) Files(id string) ([]engine.File, error) {
	t, err := m.torrentOf(id)
	if err != nil {
		return nil, err
	}
	if fs := t.Files(); fs != nil {
		return fs, nil
	}
	return nil, fmt.Errorf("metadata not available yet")
}

// SetFilePriority sets skip, normal or high for one file, now and after restarts.
func (m *Manager) SetFilePriority(id string, index int, prio string) error {
	p, ok := filePriority[prio]
	if !ok {
		return fmt.Errorf("priority must be skip, normal or high")
	}
	files, err := m.Files(id)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(files) {
		return fmt.Errorf("file index %d out of range", index)
	}
	t, _ := m.torrentOf(id)
	t.SetFilePriority(index, p)
	j, err := m.get(id)
	if err != nil {
		return err
	}
	var meta torMeta
	json.Unmarshal(j.Meta, &meta)
	if meta.Files == nil {
		meta.Files = map[int]string{}
	}
	meta.Files[index] = prio
	b, _ := json.Marshal(meta)
	return store.SetJob(m.o.DB, id, map[string]any{"meta": json.RawMessage(b)})
}
