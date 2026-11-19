package relay

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDeltaStoreMultiDeviceCursorsAndPurge(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	for _, tc := range []struct {
		name string
		new  func(t *testing.T) DeltaStore
	}{
		{"memory", func(t *testing.T) DeltaStore { return &memoryDeltas{} }},
		{"sqlite", func(t *testing.T) DeltaStore {
			st, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			return st
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := tc.new(t)
			s1, err := ds.AppendDelta("o1", "her:m1", []byte("e1"), now, now.Add(time.Hour))
			if err != nil || s1 != 1 {
				t.Fatalf("append 1: seq=%d err=%v", s1, err)
			}
			s2, err := ds.AppendDelta("o1", "her:m1", []byte("e2"), now, now.Add(time.Hour))
			if err != nil || s2 != 2 {
				t.Fatalf("append 2: seq=%d err=%v", s2, err)
			}
			s3, err := ds.AppendDelta("o1", "her:m2", []byte("e3"), now, now.Add(time.Minute))
			if err != nil || s3 != 1 {
				t.Fatalf("append 3: seq=%d err=%v", s3, err)
			}

			// Device A pulls from 0 -> gets all 3 without deleting them.
			gotA, moreA, err := ds.PullDeltas("o1", nil, false, 64, now)
			if err != nil || moreA || len(gotA) != 3 {
				t.Fatalf("device A: len=%d more=%v err=%v", len(gotA), moreA, err)
			}
			// Device B pulls from her:m1=1 -> gets m1#2 and m2#1 independently.
			gotB, moreB, err := ds.PullDeltas("o1", map[string]uint64{"her:m1": 1}, false, 64, now)
			if err != nil || moreB || len(gotB) != 2 {
				t.Fatalf("device B: len=%d more=%v err=%v", len(gotB), moreB, err)
			}
			if gotB[0].StreamID != "her:m1" || gotB[0].Seq != 2 || gotB[1].StreamID != "her:m2" || gotB[1].Seq != 1 {
				t.Fatalf("device B order/seqs: %+v", gotB)
			}
			// Pagination limit sets hasMore=true.
			page, more, err := ds.PullDeltas("o1", nil, false, 2, now)
			if err != nil || !more || len(page) != 2 {
				t.Fatalf("page: len=%d more=%v err=%v", len(page), more, err)
			}
			// Heads report latest seq per stream.
			heads, err := ds.DeltaHeads("o1", now)
			if err != nil || heads["her:m1"] != 2 || heads["her:m2"] != 1 {
				t.Fatalf("heads: %v err=%v", heads, err)
			}
			// After 2 minutes, her:m2 expired while her:m1 stays.
			later := now.Add(2 * time.Minute)
			if err := ds.PurgeDeltas(later); err != nil {
				t.Fatal(err)
			}
			rem, _, err := ds.PullDeltas("o1", nil, false, 64, later)
			if err != nil || len(rem) != 2 {
				t.Fatalf("after purge: len=%d err=%v", len(rem), err)
			}
		})
	}
}
