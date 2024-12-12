package trace

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"slices"

	"golang.org/x/exp/trace"
)

const (
	traceAllocFreeTypesBatch = iota // Contains types. [{id, address, size, ptrspan, name length, name string} ...]
	traceAllocFreeInfoBatch         // Contains info for interpreting events. [min heap addr, page size, min heap align, min stack align]
)

type traceEv uint8

const (
	_ traceEv = 127 + iota

	// Experimental events for ExperimentAllocFree.

	// Experimental heap span events. IDs map reversibly to base addresses.
	traceEvSpan      // heap span exists [timestamp, id, npages, type/class]
	traceEvSpanAlloc // heap span alloc [timestamp, id, npages, type/class]
	traceEvSpanFree  // heap span free [timestamp, id]

	// Experimental heap object events. IDs map reversibly to addresses.
	traceEvHeapObject      // heap object exists [timestamp, id, type]
	traceEvHeapObjectAlloc // heap object alloc [timestamp, id, type]
	traceEvHeapObjectFree  // heap object free [timestamp, id]

	// Experimental goroutine stack events. IDs map reversibly to addresses.
	traceEvGoroutineStack      // stack exists [timestamp, id, order]
	traceEvGoroutineStackAlloc // stack alloc [timestamp, id, order]
	traceEvGoroutineStackFree  // stack free [timestamp, id]
)

type TypeMeta struct {
	Id       int
	Ptr      uint64
	Size     int
	PtrBytes int
	Name     string
}

type TraceInfo struct {
	MinPageHeapAddr uint64
	PageSize        uint64
	MinHeapAlign    uint64
	FixedStack      uint64
}

type HeapObject struct {
	id  uint64
	typ int

	time trace.Time
}

type PtrObjectMapping struct {
	Ptr      uint64
	TypeName string
	Size     int
}

func (m PtrObjectMapping) AddrString() string {
	return fmt.Sprintf("0x%x", m.Ptr)
}

func (h HeapObject) AddrString(t TraceInfo) string {
	return fmt.Sprintf("0x%x", ((h.id * 8) + t.MinPageHeapAddr))
}

func (h HeapObject) Addr(t TraceInfo) uint64 {
	return ((h.id * 8) + t.MinPageHeapAddr)
}

func (h HeapObject) Type(t map[int]TypeMeta) string {
	v, ok := t[h.typ]
	if !ok {
		return "???"
	}
	return v.Name
}

func (h HeapObject) Size(t map[int]TypeMeta) int {
	v, ok := t[h.typ]
	if !ok {
		return 0
	}
	return v.Size
}

func (h HeapObject) HasName(t map[int]TypeMeta) bool {
	_, ok := t[h.typ]
	return ok
}

func ParseTrace(r io.Reader, debug bool, onlyExistingObjects bool) ([]PtrObjectMapping, error) {

	if debug {
		log.Println("Debug mode enabled")
	}

	tr, err := trace.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("trace.NewReader: %w", err)
	}

	var allocFreeInfo TraceInfo
	typeMap := map[int]TypeMeta{}
	allocs := map[uint64]HeapObject{}

	for {
		ev, err := tr.ReadEvent()
		if err == io.EOF {
			break
		}

		if ev.Kind() != trace.EventExperimental {
			continue
		}
		expEvent := ev.Experimental()

		switch expEvent.Name {
		case "HeapObject":
			id := expEvent.Args[0]
			typ := expEvent.Args[1]
			h := HeapObject{id: id, typ: int(typ), time: ev.Time()}
			allocs[id] = h
		case "HeapObjectAlloc":
			if onlyExistingObjects {
				continue
			}
			id := expEvent.Args[0]
			typ := expEvent.Args[1]
			h := HeapObject{id: id, typ: int(typ), time: ev.Time()}
			allocs[id] = h
		case "HeapObjectFree":
			if onlyExistingObjects {
				continue
			}
			id := expEvent.Args[0]
			delete(allocs, id)
		case "Span":
			expData := expEvent.Data
			if expData == nil {
				return nil, fmt.Errorf("expData is nil")
			}
			for _, b := range expData.Batches {
				data := b.Data
				if data[0] == 1 {
					allocFreeInfo = parseAllocFreeInfo(data)
					if debug {
						log.Printf("AllocFreeInfo: %+v\n", allocFreeInfo)
					}
				} else if data[0] == 0 {
					types := parseAllocFreeTypes(data)
					for _, t := range types {
						if debug {
							log.Printf("Type: %+v\n", t)
						}
						typeMap[t.Id] = t
					}
				}
			}
		}
	}

	if debug {
		log.Printf("AllocFree Info: %+v\n", allocFreeInfo)
	}

	allocsSlice := make([]HeapObject, 0, len(allocs))
	for _, h := range allocs {
		allocsSlice = append(allocsSlice, h)
	}
	slices.SortFunc(allocsSlice, func(a, b HeapObject) int {
		return int(a.time - b.time)
	})

	out := make([]PtrObjectMapping, 0, len(allocsSlice))
	for _, h := range allocsSlice {
		if h.HasName(typeMap) {
			out = append(out, PtrObjectMapping{
				Ptr:      h.Addr(allocFreeInfo),
				TypeName: h.Type(typeMap),
				Size:     h.Size(typeMap),
			})
		}
	}
	return out, nil
}

func parseAllocFreeInfo(inputData []byte) TraceInfo {
	assert(inputData[0] == 1) // Meta info
	inputData = inputData[1:]

	var trace TraceInfo

	var n int
	trace.MinPageHeapAddr, n = binary.Uvarint(inputData)
	if n <= 0 {
		panic("failed to read varint for MinPageHeapAddr")
	}
	inputData = inputData[n:]

	trace.PageSize, n = binary.Uvarint(inputData)
	if n <= 0 {
		panic("failed to read varint for PageSize")
	}
	inputData = inputData[n:]

	trace.MinHeapAlign, n = binary.Uvarint(inputData)
	if n <= 0 {
		panic("failed to read varint for MinHeapAlign")
	}
	inputData = inputData[n:]

	trace.FixedStack, n = binary.Uvarint(inputData)
	if n <= 0 {
		panic("failed to read varint for FixedStack")
	}
	inputData = inputData[n:]

	return trace
}

func parseAllocFreeTypes(inputData []byte) []TypeMeta {
	assert(inputData[0] == 0) // Type info
	inputData = inputData[1:]
	var out []TypeMeta

	for len(inputData) > 0 {
		meta := TypeMeta{}
		nodeID, n := binary.Uvarint(inputData) // Node ID
		if n <= 0 {
			panic("invalid node ID")
		}
		inputData = inputData[n:]
		meta.Id = int(nodeID)

		typPtr, n := binary.Uvarint(inputData)
		if n <= 0 {
			panic("failed to read varint for typPtr")
		}
		inputData = inputData[n:]
		meta.Ptr = typPtr

		size, n := binary.Uvarint(inputData)
		if n <= 0 {
			panic("failed to read varint for size")
		}
		inputData = inputData[n:]
		meta.Size = int(size)

		ptrBytes, n := binary.Uvarint(inputData)
		if n <= 0 {
			panic("failed to read varint for ptrBytes")
		}
		inputData = inputData[n:]
		meta.PtrBytes = int(ptrBytes)

		nameLen, n := binary.Uvarint(inputData)
		if n <= 0 {
			panic("failed to read varint for nameLen")
		}
		inputData = inputData[n:]

		typName := string(inputData[:nameLen])
		inputData = inputData[nameLen:]

		meta.Name = typName

		out = append(out, meta)
	}

	// sort by ID
	slices.SortFunc(out, func(a, b TypeMeta) int {
		return int(a.Id - b.Id)
	})

	return out
}

func assert(cond bool) {
	if !cond {
		panic("assertion failed")
	}
}
