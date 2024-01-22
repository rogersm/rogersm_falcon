package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	pb "rogersm_falcon/firmware"

	"google.golang.org/protobuf/encoding/prototext"
)

const VERSION = "2.1.0"

// Looking at the top of the Max Falcon-8:
//
// | 1 | 2 | 3 | 4 |
// |---|---|---|---|
// | 5 | 6 | 7 | 8 |
const (
	// Button offsets.
	loc_b1 = 0x5189
	loc_b2 = 0x5181
	loc_b3 = 0x5179
	loc_b4 = 0x5149
	loc_b5 = 0x518a
	loc_b6 = 0x5182
	loc_b7 = 0x517a
	loc_b8 = 0x514a

	// Program offsets.
	loc_prog1 = 0x539c
	loc_prog2 = 0x56bc
	loc_prog3 = 0x59dc
	loc_prog4 = 0x5cfc
	loc_prog5 = 0x601c
	loc_prog6 = 0x633c
	loc_prog7 = 0x665c
	loc_prog8 = 0x697c

	// This "key," when assigned to the button signifies that the button is not an
	// individual key but a Program.
	key_prog1 = 0xd7
	key_prog2 = 0xd8
	key_prog3 = 0xd9
	key_prog4 = 0xda
	key_prog5 = 0xdb
	key_prog6 = 0xdc
	key_prog7 = 0xdd
	key_prog8 = 0xde

	fd_stop = 0xfd
)

var DEBUG bool

// Debug if enabled
func DebugPrintf(format string, a ...any) {
	if DEBUG {
		fmt.Printf(format+"\n", a...)
	}
}

// writeByteAtOffset writes byte b at offset of w. No bounds checking is done.
func writeByteAtOffset(w *[]byte, b byte, offset uint) {
	DebugPrintf("    writeByteAtOffset byte=0x%x, offset=0x%x", b, offset)
	(*w)[offset] = b

}

// writeProgramAtOffset serializes a prog's ProgramSet messages at offset of w.
func writeProgramAtOffset(w *[]byte, prog *pb.Program, offset uint) {

	DebugPrintf("writeProgramAtOffset prog=%v offset=%v", prog, offset)

	var i int
	var ps *pb.ProgramSet

	// Now write the program at the program offset.
	for i, ps = range prog.GetProgramSet() {
		DebugPrintf("Writing Program #%d", i)
		// 8 bytes per program set.
		progset_start := offset + uint(i*8)

		DebugPrintf("Writing GetModifier (program #%d):", i)
		writeByteAtOffset(w, byte(ps.GetModifier()), progset_start)
		DebugPrintf("Writing GetMillisecondsBetweenKeys (program #%d):", i)
		writeByteAtOffset(w, byte(ps.GetMillisecondsBetweenKeys()), progset_start+1)

		DebugPrintf("Writing Program code:")

		keys := ps.GetKeys()
		for j := 0; j < 6; j++ { // 6 characters max per program set. HID packet can send 6 events in a single packet.
			// Defaults to 0 (0x00).
			var key pb.HIDKeyboardKey

			// This is guarded; if false, key will be 0x00, and we will right pad the
			// keys with 0x00. This is to avoid stale keys remaining there from the
			// existing firmware programming.
			if j < len(keys) {
				key = keys[j]
			}

			writeByteAtOffset(w, byte(key), progset_start+2+uint(j))
		}
	}

	// Finish with a 'FD' type program set. These are sets that start with 0xFD,
	// which signify to the MAX Falcon firmware the end of the program.
	i += 1
	progset_start := offset + uint(i*8)
	DebugPrintf("Writing Finish with a FD type program set:")
	writeByteAtOffset(w, byte(fd_stop), progset_start)

	// Fill the remaining bytes of the remaining program sets with 0x00. For the
	// math involved here, see firmware-format.md.
	padWithZeroes(progset_start+1, offset, w)
}

// Fill the remaining bytes of the remaining program sets with 0x00. For the
// math involved here, see firmware-format.md.
func padWithZeroes(progset_start uint, offset uint, w *[]byte) {

	var mem_pos uint

	DebugPrintf("Padding remaining program bytes with 0x00:")

	DebugPrintf("    start writeZeroAtOffset byte=0x%x, offset=0x%x", byte(0x00), progset_start)

	for mem_pos = progset_start; mem_pos < offset+800-8; mem_pos += 1 {
		(*w)[mem_pos] = byte(0x00)
	}

	DebugPrintf("    end   writeZeroAtOffset byte=0x%x, offset=0x%x", byte(0x00), mem_pos)

}

// writeFirmware writes the verified bindings to w.
func writeFirmware(w *[]byte, bindings *pb.ButtonBindings) {
	// bs creates a mapping of Binding getter, button offset, program key to use
	// (see key_prog# above), and program offset.
	bs := []struct {
		binding  func() *pb.ButtonBinding
		boff     uint // Button offset.
		key_prog uint // Button # to program key. These are the special constants defined
		progoff  uint // Program offset.
	}{
		{bindings.GetButton1, loc_b1, key_prog1, loc_prog1},
		{bindings.GetButton2, loc_b2, key_prog2, loc_prog2},
		{bindings.GetButton3, loc_b3, key_prog3, loc_prog3},
		{bindings.GetButton4, loc_b4, key_prog4, loc_prog4},
		{bindings.GetButton5, loc_b5, key_prog5, loc_prog5},
		{bindings.GetButton6, loc_b6, key_prog6, loc_prog6},
		{bindings.GetButton7, loc_b7, key_prog7, loc_prog7},
		{bindings.GetButton8, loc_b8, key_prog8, loc_prog8},
	}

	// Bindings have already been verified, so no need to check errors.
	for i, b := range bs {
		binding := b.binding()
		DebugPrintf("Button #%d, offset = 0x%x", i, b.boff)

		if binding.GetKey() != pb.HIDKeyboardKey_NULL {
			// GetKey() has already been validated, so we can be assured its length is
			// 1.
			writeByteAtOffset(w, byte(b.binding().GetKey()), b.boff)
		} else if binding.GetString_() != "" {
			// GetString_() has already been verified, so we can be assured its length
			// 1.

			// Already validated, so no need to check error.
			k, _ := StringToHID(b.binding().GetString_()[0])
			writeByteAtOffset(w, byte(k), b.boff)

		} else if prog := binding.GetProgram(); prog != nil {
			// This signifies that the button points to a Program.
			writeByteAtOffset(w, byte(b.key_prog), b.boff)

			writeProgramAtOffset(w, prog, b.progoff)

		} else {

			// Should never get here, because the binding has been verified already.
			panic(fmt.Sprintf("invalid binding state: %v", binding))
		}
	}
}

func main() {

	var fpath string               // The firmware.bin file path.
	var text_proto string          // The path to the proto in textproto format.
	var verify_only bool           // Whether to verify the proto only (do no firmware writing).
	var showhelp, showversion bool // show help and version

	flag.BoolVar(&verify_only, "verify_only", false, "verify the -text_proto only (do no firmware writing).")
	flag.StringVar(&text_proto, "text_proto", "", "the path to the serialized proto in textproto format.")
	flag.StringVar(&fpath, "firmware_bin_path", "", "path to the firmware on the device; modified in-place.")
	flag.BoolVar(&showhelp, "help", false, "show this help")
	flag.BoolVar(&showversion, "version", false, "show current software version")
	flag.BoolVar(&DEBUG, "debug", false, "show debug messages")

	flag.Parse()

	if showversion {
		fmt.Fprintln(os.Stderr, VERSION)
		os.Exit(0)
	}

	if showhelp {
		flag.PrintDefaults()
		os.Exit(1)
	}

	// we only can run the program if
	// text_proto != "" && fpath != ""
	// text_proto != "" && verify_only

	// so, if we don't meet these conditions we show which parameters are needed:

	if !(text_proto != "" && fpath != "" ||
		text_proto != "" && verify_only) {
		flag.PrintDefaults()
		os.Exit(1)
	}

	tp, err := os.ReadFile(text_proto)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	bindings := pb.ButtonBindings{}

	if err = prototext.Unmarshal(tp, &bindings); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing proto: %v\n", err)
		os.Exit(1)
	}

	_, err = prototext.Marshal(&bindings)

	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshaling proto: %v\n", err)
		os.Exit(1)
	}

	DebugPrintf("Input file:\n %s", prototext.Format(&bindings))

	if err = VerifyButtonBindings(&bindings); err != nil {
		fmt.Fprintf(os.Stderr, "error verifying button bindings: %v\n", err)
		os.Exit(1)
	}

	if verify_only {
		os.Exit(0)
	}

	f, err := os.OpenFile(fpath, os.O_RDWR, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to open %s (%s)", fpath, err)
		os.Exit(1)
	}
	defer f.Close()
	defer f.Sync()

	// Manipulate the bytes from the firmware in-memory, without resorting to
	// seeking around the actual file on disk and rewriting individual bytes
	// there. The firmware is simply not valid and exhibits problems if it is
	// modified in seek/write/seek/write way. This is some subtle behavior
	// discovered after much pain.
	//
	// This also makes it so that the entire firmware is written all at once with
	// one Write(), which is closer to what the official programmer does (verified
	// with careful scrutiny of `strace` runs).
	b, err := io.ReadAll(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to read %s (%s)", fpath, err)
		os.Exit(1)
	}

	writeFirmware(&b, &bindings)

	f.Seek(int64(0), io.SeekStart)
	n, err := f.Write(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to write %s (%s)", fpath, err)
		os.Exit(1)
	}
	fmt.Println("Bytes written:", n)

	os.Exit(0)
}
