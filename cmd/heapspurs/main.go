package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/adamroach/heapspurs/internal/pkg/config"
	"github.com/adamroach/heapspurs/pkg/heapdump"
	"github.com/adamroach/heapspurs/pkg/trace"
	"github.com/adamroach/heapspurs/pkg/treeclimber"
)

func main() {
	conf, err := config.Initialize()
	if err != nil {
		panic(fmt.Sprintf("Config: %v\n", err))
	}

	hasMallocMeta := len(conf.MallocMeta) > 0
	hasTrace := len(conf.Trace) > 0

	if hasMallocMeta && hasTrace {
		log.Fatal("Cannot specify both MallocMeta and Trace")
	}
	if hasTrace {
		file, err := os.Open(conf.Trace)
		if err != nil {
			panic(fmt.Sprintf("Open Trace file '%s': %v\n", conf.Oid, err))
		}

		mappings, err := trace.ParseTrace(file, false, false)
		if err != nil {
			panic(fmt.Sprintf("failed to parse: %v\n", err))
		}

		for _, m := range mappings {
			heapdump.AddName(m.Ptr, m.TypeName)
			heapdump.AddNameWithSize(m.Ptr, m.Size, m.TypeName)
		}

		file.Close()
	}

	if hasMallocMeta {
		file, err := os.Open(conf.MallocMeta)
		if err != nil {
			panic(fmt.Sprintf("Open MallocMeta file '%s': %v\n", conf.Oid, err))
		}

		s := bufio.NewScanner(file)
		s.Split(bufio.ScanLines)
		for s.Scan() {
			txt := s.Text()
			parts := strings.Split(txt, ": ")
			ptr, err := strconv.ParseUint(parts[0][2:], 16, 64)
			if err != nil {
				panic(fmt.Sprintf("Error parsing '%s' as hex: %v\n", parts[0], err))
			}

			lastSpace := strings.LastIndex(parts[1], " ")
			name := parts[1][:lastSpace]

			size, err := strconv.ParseUint(parts[1][lastSpace+1:], 10, 64)
			if err != nil {
				panic(fmt.Sprintf("Error parsing size '%s': %v\n", parts[2], err))
			}
			heapdump.AddName(ptr, name)
			heapdump.AddNameWithSize(ptr, int(size), name)
		}
		file.Close()
	}

	if len(conf.Oid) > 0 {
		file, err := os.Open(conf.Oid)
		if err != nil {
			panic(fmt.Sprintf("Open OID file '%s': %v\n", conf.Oid, err))
		}
		err = heapdump.ReadOids(file)
		if err != nil {
			panic(fmt.Sprintf("Reading OID file '%s': %v\n", conf.Oid, err))
		}
		file.Close()
	}

	if len(conf.Program) > 0 {
		cmd := exec.Command("go", "tool", "nm", conf.Program)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			panic(fmt.Sprintf("Open program file '%s': %v\n", conf.Program, err))
		}
		err = cmd.Start()
		if err != nil {
			panic(fmt.Sprintf("Running [go tool nm] on '%s': %v\n", conf.Program, err))
		}
		if err != nil {
			panic(fmt.Sprintf("Open program file '%s': %v\n", conf.Program, err))
		}
		err = heapdump.ReadSymbols(stdout)
		if err != nil {
			panic(fmt.Sprintf("Reading program file '%s': %v\n", conf.Program, err))
		}
		cmd.Wait()
	}

	file, err := os.Open(conf.Dumpfile)
	if err != nil {
		panic(fmt.Sprintf("Open '%s': %v\n", conf.Dumpfile, err))
	}
	reader := bufio.NewReader(file)

	if conf.Print {
		err = heapdump.PrintRecords(reader, "")
		if err != nil {
			panic(err)
		}
		return
	}

	if len(conf.Find) > 0 {
		err = heapdump.PrintRecords(reader, conf.Find)
		if err != nil {
			panic(err)
		}
		return
	}

	climber, err := treeclimber.NewTreeClimber(reader)

	if len(conf.MakeDump) > 0 {
		f, err := os.Create(conf.MakeDump)
		if err != nil {
			panic("Could not open file for writing:" + err.Error())
		} else {
			runtime.GC()
			debug.WriteHeapDump(f.Fd())
			f.Close()
		}
		return
	}

	if err != nil {
		panic(err)
	}
	file.Close()

	if conf.Anchors {
		err := climber.PrintAnchors(conf.Address)
		if err != nil {
			panic(err)
		}
		return
	}

	if conf.Owners != 0 {
		err := climber.PrintOwners(conf.Address, conf.Owners)
		if err != nil {
			panic(err)
		}
		return
	}

	if conf.Hexdump {
		hexdump, err := climber.Hexdump(conf.Address)
		if err != nil {
			panic(err)
		}
		fmt.Print(hexdump)
		return
	}

	out, err := os.Create(conf.Output)
	if err != nil {
		panic(fmt.Sprintf("Create '%s': %v\n", conf.Output, err))
	}
	climber.WriteSVG(conf.Address, out)
	out.Close()
}
