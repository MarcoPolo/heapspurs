package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"time"

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

type HeapObjectAlloc struct {
	id  uint64
	typ int

	time trace.Time
}

func (h HeapObjectAlloc) Addr(t TraceInfo) string {
	return fmt.Sprintf("0x%x", ((h.id * 8) + t.MinPageHeapAddr))
}

func (h HeapObjectAlloc) Type(t map[int]TypeMeta) string {
	v, ok := t[h.typ]
	if !ok {
		return "???"
	}
	return v.Name
}

func (h HeapObjectAlloc) Size(t map[int]TypeMeta) int {
	v, ok := t[h.typ]
	if !ok {
		return 0
	}
	return v.Size
}

func (h HeapObjectAlloc) HasName(t map[int]TypeMeta) bool {
	_, ok := t[h.typ]
	return ok
}

const debug = false

type bytesReadReader struct {
	r         io.Reader
	bytesRead int
}

func (b *bytesReadReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.bytesRead += n
	return n, err

}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <trace.bin>\n\n", os.Args[0])

		os.Exit(1)
	}

	traceFile := os.Args[1]

	fileSizeBytes := -1
	var bytesReadPtr *int
	var r io.Reader
	if traceFile == "-" {
		r = os.Stdin
		bytesReadPtr = &fileSizeBytes
	} else {
		f, err := os.Open(traceFile)
		if err != nil {
			fmt.Println("Error opening file:", err)
			os.Exit(1)
		}

		if fileInfo, err := f.Stat(); err == nil {
			fileSizeBytes = int(fileInfo.Size())
		}

		rdr := &bytesReadReader{r: f}
		r = rdr
		bytesReadPtr = &rdr.bytesRead

		defer f.Close()
	}

	tr, err := trace.NewReader(r)
	if err != nil {
		log.Fatal("trace.NewReader", err)
	}

	var allocFreeInfo TraceInfo
	typeMap := map[int]TypeMeta{}
	allocs := map[uint64]HeapObjectAlloc{}

	start := time.Now()

	loopIters := 0
	for {
		if loopIters%10_000_000 == 0 {
			pctDone := (*bytesReadPtr * 100) / fileSizeBytes
			log.Printf("Parsing... %d/%d %d%%\n", *bytesReadPtr, fileSizeBytes, pctDone)
		}
		loopIters++

		ev, err := tr.ReadEvent()
		if err == io.EOF {
			break
		}

		if ev.Kind() != trace.EventExperimental {
			continue
		}
		expEvent := ev.Experimental()

		switch expEvent.Name {
		case "HeapObjectAlloc":
			id := expEvent.Args[0]
			typ := expEvent.Args[1]
			h := HeapObjectAlloc{id: id, typ: int(typ), time: ev.Time()}
			allocs[id] = h
		case "HeapObjectFree":
			id := expEvent.Args[0]
			delete(allocs, id)
		case "Span":
			expData := expEvent.Data
			if expData == nil {
				log.Fatal("expData is nil")
			}
			for _, b := range expData.Batches {
				data := b.Data
				if data[0] == 1 {
					allocFreeInfo = parseAllocFreeInfo(data)
					if debug {
						fmt.Printf("AllocFreeInfo: %+v\n", allocFreeInfo)
					}
				} else if data[0] == 0 {
					types := parseAllocFreeTypes(data)
					for _, t := range types {
						if debug {
							fmt.Printf("Type: %+v\n", t)
						}
						typeMap[t.Id] = t
					}
				}
			}
		}
	}

	log.Printf("Done parsing: %v\n", time.Since(start))
	start = time.Now()

	if debug {
		fmt.Printf("AllocFree Info: %+v\n", allocFreeInfo)
	}

	allocsSlice := make([]HeapObjectAlloc, 0, len(allocs))
	for _, h := range allocs {
		allocsSlice = append(allocsSlice, h)
	}
	slices.SortFunc(allocsSlice, func(a, b HeapObjectAlloc) int {
		return int(a.time - b.time)
	})
	log.Printf("Done sorting: %v\n", time.Since(start))
	for _, h := range allocsSlice {
		if h.HasName(typeMap) {
			fmt.Printf("%s: %s %d\n", h.Addr(allocFreeInfo), h.Type(typeMap), h.Size(typeMap))
		}
	}

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
