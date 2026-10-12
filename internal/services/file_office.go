package services

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/richardlehane/mscfb"
)

// legacyOfficeType follows the compound-file directory and stream allocation
// chains, rather than guessing from a prefix or the supplied extension. Directory
// entries can legitimately be at the end of the file. Embedded documents do not
// determine the outer document's media type. Nothing is extracted to disk.
func legacyOfficeType(path string) (string, error) {
	invalid := fmt.Errorf("invalid file content: malformed or unsupported legacy Office document")
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	var header [512]byte
	if _, err := f.ReadAt(header[:], 0); err != nil {
		return "", invalid
	}
	version, shift := binary.LittleEndian.Uint16(header[26:]), binary.LittleEndian.Uint16(header[30:])
	if !(version == 3 && shift == 9 || version == 4 && shift == 12) {
		return "", invalid
	}
	sectors := uint64(stat.Size() >> shift)
	// Bound parser allocations by physically present sectors, not untrusted
	// directory/FAT counts. This also rejects truncated allocation structures.
	for _, offset := range []int{40, 44, 64, 72} {
		if uint64(binary.LittleEndian.Uint32(header[offset:])) > sectors {
			return "", invalid
		}
	}
	compound, err := mscfb.New(f)
	if err != nil {
		return "", invalid
	}
	streams := map[string][]byte{}
	var total int64
	buffer := make([]byte, 32<<10)
	for _, entry := range compound.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Size < 0 || entry.Size > stat.Size()-total {
			return "", invalid
		}
		total += entry.Size
		if entry.Size == 0 {
			continue
		}
		if len(entry.Path) == 0 && (entry.Name == "WordDocument" || entry.Name == "0Table" || entry.Name == "1Table" || entry.Name == "Workbook" || entry.Name == "Book" || entry.Name == "PowerPoint Document" || entry.Name == "Current User") {
			if _, exists := streams[entry.Name]; exists {
				return "", invalid
			}
			prefix := make([]byte, min(entry.Size, 32))
			if _, err := entry.ReadAt(prefix, 0); err != nil {
				return "", invalid
			}
			streams[entry.Name] = prefix
		}
		if _, err := entry.Seek(0, io.SeekStart); err != nil {
			return "", invalid
		}
		if n, err := io.CopyBuffer(io.Discard, io.LimitReader(entry, entry.Size), buffer); err != nil || n != entry.Size {
			return "", invalid
		}
	}
	found := ""
	matches := 0
	if word := streams["WordDocument"]; len(word) >= 32 && binary.LittleEndian.Uint16(word) == 0xa5ec {
		table := "0Table"
		if binary.LittleEndian.Uint16(word[10:])&0x0200 != 0 {
			table = "1Table"
		}
		// Word 97+ selects a table stream in its FIB. Older Word versions
		// store those tables in WordDocument itself.
		if binary.LittleEndian.Uint16(word[2:]) < 0xc1 || len(streams[table]) > 0 {
			found = "application/msword"
			matches++
		}
	}
	for _, name := range []string{"Workbook", "Book"} {
		if book := streams[name]; len(book) >= 8 && binary.LittleEndian.Uint16(book) == 0x0809 && binary.LittleEndian.Uint16(book[6:]) == 0x0005 {
			version := binary.LittleEndian.Uint16(book[4:])
			if version == 0x0600 || version == 0x0500 {
				found = "application/vnd.ms-excel"
				matches++
			}
		}
	}
	if ppt := streams["PowerPoint Document"]; len(ppt) >= 8 && len(streams["Current User"]) >= 8 {
		typ := binary.LittleEndian.Uint16(ppt[2:])
		if typ == 1000 || typ == 4085 {
			found = "application/vnd.ms-powerpoint"
			matches++
		}
	}
	if matches != 1 {
		return "", invalid
	}
	return found, nil
}
