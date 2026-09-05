// Package releaseassets validates platform resources embedded in release binaries.
package releaseassets

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"image/png"
)

const (
	peResourceDirectoryIndex = 2
	peResourceIcon           = 3
	peResourceGroupIcon      = 14
	resourceSubdirectoryFlag = uint32(1 << 31)
)

// VerifyPEIcon checks that a Windows executable contains a structurally usable
// grouped icon whose entries reference matching icon resources. Explorer uses
// these resources for the executable icon.
func VerifyPEIcon(path string) error {
	file, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("open PE executable: %w", err)
	}
	defer file.Close()

	resourceRVA, err := peResourceRVA(file)
	if err != nil {
		return err
	}
	section := sectionForRVA(file, resourceRVA)
	if section == nil {
		return errors.New("PE resource directory is outside every section")
	}
	data, err := section.Data()
	if err != nil {
		return fmt.Errorf("read PE resource section: %w", err)
	}
	rootOffset := uint64(resourceRVA - section.VirtualAddress)
	icons := resourceTypePayloads(data, rootOffset, section.VirtualAddress, peResourceIcon)
	if len(icons) == 0 {
		return fmt.Errorf("PE executable is missing resource type %d", peResourceIcon)
	}
	groups := resourceTypePayloads(data, rootOffset, section.VirtualAddress, peResourceGroupIcon)
	if len(groups) == 0 {
		return fmt.Errorf("PE executable is missing resource type %d", peResourceGroupIcon)
	}
	var groupErr error
	for _, localizedGroups := range groups {
		for _, group := range localizedGroups {
			if err := validateGroupIcon(group, icons); err == nil {
				return nil
			} else {
				groupErr = err
			}
		}
	}
	return fmt.Errorf("PE executable has no usable grouped icon: %w", groupErr)
}

func peResourceRVA(file *pe.File) (uint32, error) {
	var directories []pe.DataDirectory
	switch header := file.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		directories = header.DataDirectory[:]
	case *pe.OptionalHeader64:
		directories = header.DataDirectory[:]
	default:
		return 0, errors.New("PE executable has no supported optional header")
	}
	if len(directories) <= peResourceDirectoryIndex || directories[peResourceDirectoryIndex].VirtualAddress == 0 || directories[peResourceDirectoryIndex].Size == 0 {
		return 0, errors.New("PE executable has no resource directory")
	}
	return directories[peResourceDirectoryIndex].VirtualAddress, nil
}

func sectionForRVA(file *pe.File, rva uint32) *pe.Section {
	for _, section := range file.Sections {
		size := section.VirtualSize
		if section.Size > size {
			size = section.Size
		}
		if rva >= section.VirtualAddress && uint64(rva-section.VirtualAddress) < uint64(size) {
			return section
		}
	}
	return nil
}

func resourceTypePayloads(data []byte, rootOffset uint64, sectionRVA, resourceType uint32) map[uint32][][]byte {
	entries, ok := resourceDirectoryEntries(data, rootOffset)
	if !ok {
		return nil
	}
	for _, entry := range entries {
		name := binary.LittleEndian.Uint32(entry[:4])
		target := binary.LittleEndian.Uint32(entry[4:])
		if name&resourceSubdirectoryFlag != 0 || name != resourceType || target&resourceSubdirectoryFlag == 0 {
			continue
		}
		return resourceDirectoryPayloads(data, rootOffset+uint64(target&^resourceSubdirectoryFlag), rootOffset, sectionRVA)
	}
	return nil
}

func resourceDirectoryPayloads(data []byte, directoryOffset, rootOffset uint64, sectionRVA uint32) map[uint32][][]byte {
	entries, ok := resourceDirectoryEntries(data, directoryOffset)
	if !ok {
		return nil
	}
	result := make(map[uint32][][]byte)
	for _, entry := range entries {
		name := binary.LittleEndian.Uint32(entry[:4])
		if name&resourceSubdirectoryFlag != 0 {
			continue
		}
		target := binary.LittleEndian.Uint32(entry[4:])
		visited := map[uint64]bool{rootOffset: true, directoryOffset: true}
		payloads := resourceEntryPayloads(data, target, rootOffset, sectionRVA, visited, 0)
		if len(payloads) > 0 {
			result[name] = append(result[name], payloads...)
		}
	}
	return result
}

func resourceEntryPayloads(data []byte, target uint32, rootOffset uint64, sectionRVA uint32, visited map[uint64]bool, depth int) [][]byte {
	if target&resourceSubdirectoryFlag == 0 {
		if payload, ok := resourceDataPayload(data, rootOffset+uint64(target), sectionRVA); ok {
			return [][]byte{payload}
		}
		return nil
	}
	directoryOffset := rootOffset + uint64(target&^resourceSubdirectoryFlag)
	if depth > 4 || visited[directoryOffset] {
		return nil
	}
	visited[directoryOffset] = true
	entries, ok := resourceDirectoryEntries(data, directoryOffset)
	if !ok {
		return nil
	}
	var result [][]byte
	for _, entry := range entries {
		childTarget := binary.LittleEndian.Uint32(entry[4:])
		result = append(result, resourceEntryPayloads(data, childTarget, rootOffset, sectionRVA, visited, depth+1)...)
	}
	return result
}

func resourceDataPayload(data []byte, dataEntryOffset uint64, sectionRVA uint32) ([]byte, bool) {
	if dataEntryOffset+16 > uint64(len(data)) {
		return nil, false
	}
	dataRVA := binary.LittleEndian.Uint32(data[dataEntryOffset:])
	size := binary.LittleEndian.Uint32(data[dataEntryOffset+4:])
	if size == 0 || dataRVA < sectionRVA {
		return nil, false
	}
	payloadOffset := uint64(dataRVA - sectionRVA)
	if payloadOffset+uint64(size) > uint64(len(data)) {
		return nil, false
	}
	return data[payloadOffset : payloadOffset+uint64(size)], true
}

func validateGroupIcon(group []byte, icons map[uint32][][]byte) error {
	if len(group) < 6 || binary.LittleEndian.Uint16(group[:2]) != 0 || binary.LittleEndian.Uint16(group[2:4]) != 1 {
		return errors.New("invalid group icon header")
	}
	count := int(binary.LittleEndian.Uint16(group[4:6]))
	if count == 0 || 6+count*14 > len(group) {
		return errors.New("invalid group icon entry count")
	}
	for index := 0; index < count; index++ {
		entry := group[6+index*14 : 6+(index+1)*14]
		width := iconDimension(entry[0])
		height := iconDimension(entry[1])
		expectedSize := binary.LittleEndian.Uint32(entry[8:12])
		iconID := uint32(binary.LittleEndian.Uint16(entry[12:14]))
		if entry[3] != 0 || expectedSize == 0 || iconID == 0 {
			return fmt.Errorf("invalid grouped icon entry %d", index)
		}
		matched := false
		for _, icon := range icons[iconID] {
			if uint64(len(icon)) == uint64(expectedSize) && validIconImage(icon, width, height) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("grouped icon entry %d does not reference a matching icon resource", index)
		}
	}
	return nil
}

func iconDimension(value byte) int {
	if value == 0 {
		return 256
	}
	return int(value)
}

func validIconImage(data []byte, expectedWidth, expectedHeight int) bool {
	if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != expectedWidth || config.Height != expectedHeight {
			return false
		}
		image, err := png.Decode(bytes.NewReader(data))
		return err == nil && image.Bounds().Dx() == expectedWidth && image.Bounds().Dy() == expectedHeight
	}
	if len(data) < 12 {
		return false
	}
	headerSize := binary.LittleEndian.Uint32(data[:4])
	var width, doubledHeight, planes, bitCount, paletteEntries, paletteEntrySize, extraMaskBytes uint64
	switch {
	case headerSize == 12:
		width = uint64(binary.LittleEndian.Uint16(data[4:6]))
		doubledHeight = uint64(binary.LittleEndian.Uint16(data[6:8]))
		planes = uint64(binary.LittleEndian.Uint16(data[8:10]))
		bitCount = uint64(binary.LittleEndian.Uint16(data[10:12]))
		paletteEntrySize = 3
	case headerSize >= 40 && uint64(headerSize) <= uint64(len(data)):
		signedWidth := int64(int32(binary.LittleEndian.Uint32(data[4:8])))
		signedHeight := int64(int32(binary.LittleEndian.Uint32(data[8:12])))
		if signedWidth <= 0 || signedHeight == 0 {
			return false
		}
		if signedHeight < 0 {
			signedHeight = -signedHeight
		}
		width = uint64(signedWidth)
		doubledHeight = uint64(signedHeight)
		planes = uint64(binary.LittleEndian.Uint16(data[12:14]))
		bitCount = uint64(binary.LittleEndian.Uint16(data[14:16]))
		compression := binary.LittleEndian.Uint32(data[16:20])
		if compression != 0 && compression != 3 {
			return false
		}
		if compression == 3 && headerSize == 40 {
			extraMaskBytes = 12
		}
		paletteEntries = uint64(binary.LittleEndian.Uint32(data[32:36]))
		paletteEntrySize = 4
	default:
		return false
	}
	if planes != 1 || doubledHeight%2 != 0 || width != uint64(expectedWidth) || doubledHeight/2 != uint64(expectedHeight) {
		return false
	}
	switch bitCount {
	case 1, 4, 8:
		if paletteEntries == 0 {
			paletteEntries = uint64(1) << bitCount
		}
	case 16, 24, 32:
		if paletteEntries != 0 {
			return false
		}
	default:
		return false
	}
	pixelHeight := doubledHeight / 2
	xorRowBytes := ((width*bitCount + 31) / 32) * 4
	andRowBytes := ((width + 31) / 32) * 4
	requiredSize := uint64(headerSize) + extraMaskBytes + paletteEntries*paletteEntrySize + (xorRowBytes+andRowBytes)*pixelHeight
	return requiredSize <= uint64(len(data))
}

func resourceDirectoryEntries(data []byte, start uint64) ([][8]byte, bool) {
	if start+16 > uint64(len(data)) {
		return nil, false
	}
	named := binary.LittleEndian.Uint16(data[start+12:])
	identified := binary.LittleEndian.Uint16(data[start+14:])
	count := uint64(named) + uint64(identified)
	if start+16+count*8 > uint64(len(data)) {
		return nil, false
	}
	entries := make([][8]byte, count)
	for index := range entries {
		copy(entries[index][:], data[start+16+uint64(index)*8:])
	}
	return entries, true
}
