package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
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
	dt  uint64
	id  uint64
	typ int

	order int
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

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <trace.txt>\n\n", os.Args[0])
		fmt.Printf("Generate trace.txt with `go tool -d=2 trace.bin > trace.txt\n")

		os.Exit(1)
	}

	traceTxt := os.Args[1]

	var r io.Reader
	if traceTxt == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(traceTxt)
		if err != nil {
			fmt.Println("Error opening file:", err)
			os.Exit(1)
		}
		r = f

		defer f.Close()
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<30)
	scanner.Split(bufio.ScanLines)

	var allocFreeInfo TraceInfo
	var types []TypeMeta
	typeMap := map[int]TypeMeta{}
	allocs := map[uint64]HeapObjectAlloc{}

	for scanner.Scan() {
		line := scanner.Text()
		var order int

		switch {
		case strings.HasPrefix(line, "HeapObjectAlloc"):
			parts := strings.Split(line, " ")
			dt, err := strconv.ParseUint(parts[1][len("dt="):], 10, 64)
			if err != nil {
				log.Fatal("dt", line, err)
			}
			id, err := strconv.ParseUint(parts[2][len("id="):], 10, 64)
			if err != nil {
				log.Fatal("id", line, err)
			}
			typ, err := strconv.Atoi(parts[3][len("typ3="):])
			if err != nil {
				log.Fatal("typ", line, err)
			}

			// if _, ok := allocs[id]; ok {
			// 	log.Printf("Duplicate alloc id: %d", id)
			// }
			order++
			h := HeapObjectAlloc{dt: dt, id: id, typ: typ, order: order}
			allocs[id] = h
		case strings.HasPrefix(line, "HeapObjectFree"):
			parts := strings.Split(line, " ")
			// dt, err := strconv.ParseUint(parts[1][len("dt="):], 10, 64)
			// if err != nil {
			// 	log.Fatal(err)
			// }
			id, err := strconv.ParseUint(parts[2][len("id="):], 10, 64)
			if err != nil {
				log.Fatal("failed to parse free", err, "line", line)
			}
			delete(allocs, id)
		case strings.HasPrefix(line, "ExperimentalBatch exp=1"):
			scanner.Scan()
			nextLine := scanner.Text()
			if len(nextLine) == 0 {
				// log.Fatal("empty line after ExperimentalBatch")
				continue
			}
			var data []byte
			_, err := fmt.Sscanf(nextLine, "\tdata=%q", &data)
			if err != nil {
				log.Fatal(line, " data ", nextLine, err)
			}
			if data[0] == 1 {
				allocFreeInfo = parseAllocFreeInfo(data)
			} else if data[0] == 0 {
				types = parseAllocFreeTypes(data)
				if debug {
					fmt.Println("Types:")
				}
				for _, t := range types {
					if debug {
						fmt.Printf("Type: %+v\n", t)
					}
					typeMap[t.Id] = t
				}
			} else {
				panic("unknown data type")
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatal("err scanning:", err)
	}

	if debug {
		fmt.Printf("AllocFree Info: %+v\n", allocFreeInfo)
	}

	allocsSlice := make([]HeapObjectAlloc, 0, len(allocs))
	for _, h := range allocs {
		allocsSlice = append(allocsSlice, h)
	}
	slices.SortFunc(allocsSlice, func(a, b HeapObjectAlloc) int {
		return a.order - b.order
	})
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
