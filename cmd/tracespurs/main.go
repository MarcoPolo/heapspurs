package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/adamroach/heapspurs/pkg/trace"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <trace.bin>\n\n", os.Args[0])

		os.Exit(1)
	}

	onlyExists := flag.Bool("only-existing-objects", false, "Only show currently existing object from last snapshot")
	debugFlag := flag.Bool("debug", false, "print debug logs")
	traceFileFlag := flag.String("trace", "trace.bin", "location of trace file")
	flag.Parse()

	debug := *debugFlag

	if debug {
		log.Println("Debug mode enabled")
		log.Println("onlyExists:", *onlyExists)
	}

	traceFile := *traceFileFlag

	var r io.Reader
	if traceFile == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(traceFile)
		if err != nil {
			fmt.Println("Error opening file:", err)
			os.Exit(1)
		}

		r = f
		defer f.Close()
	}

	mappings, err := trace.ParseTrace(r, debug, *onlyExists)
	if err != nil {
		log.Fatal("Error parsing trace:", err)
	}

	for _, m := range mappings {
		fmt.Printf("%s: %s %d\n", m.AddrString(), m.TypeName, m.Size)
	}
}
