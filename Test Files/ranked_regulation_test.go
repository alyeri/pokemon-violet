package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
)

func TestRankedRegulationWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		preset  int64
		id, min byte
	}{{3, 2, 3}, {6, 5, 4}} {
		record := make([]byte, 0x29c0)
		copy(record, []byte{p.id, p.min, 6, p.min, p.min})
		record[len(record)-1] = 123
		if _, err := f.WriteAt(record, 0x3c318d4+(p.preset-1)*0x29c0); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	t.Setenv("VIOLET_RANKED_MAIN_IMAGE", path)
	for _, kind := range []uint64{3, 4} {
		wire, err := marshalRankedCompetitionRegulated("test", kind, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for len(wire) > 0 {
			num, typ, n := protowire.ConsumeTag(wire)
			if n < 0 {
				t.Fatal("bad tag")
			}
			wire = wire[n:]
			if num == 24 {
				data, n := protowire.ConsumeBytes(wire)
				if n < 0 {
					t.Fatal("bad application data")
				}
				var m commonpb.MapValue
				if err := proto.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				r := m.Fields["Regulation"].GetBytesValue()
				id, min := byte(2), byte(3)
				if kind == 4 {
					id, min = 5, 4
				}
				if len(m.Fields) != 1 || len(r) != 0x29c0 || !bytes.Equal(r[:5], []byte{id, min, 6, min, min}) || r[len(r)-1] != 123 {
					t.Fatal("wrong regulation payload")
				}
				found = true
			}
			n = protowire.ConsumeFieldValue(num, typ, wire)
			if n < 0 {
				t.Fatal("bad field")
			}
			wire = wire[n:]
		}
		if !found {
			t.Fatal("missing application_data")
		}
	}
	t.Setenv("VIOLET_RANKED_MAIN_IMAGE", filepath.Join(t.TempDir(), "missing"))
	if _, err := marshalRankedCompetitionRegulated("test", 3, time.Now()); err == nil {
		t.Fatal("missing image accepted")
	}
}
