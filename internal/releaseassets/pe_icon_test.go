package releaseassets

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestResourcePayloadsFormUsableGroupedIcon(t *testing.T) {
	data := make([]byte, 1024)
	putResourceDirectory(t, data, 0, [][2]uint32{
		{peResourceIcon, resourceSubdirectoryFlag | 64},
		{peResourceGroupIcon, resourceSubdirectoryFlag | 96},
	})
	putResourceDirectory(t, data, 64, [][2]uint32{{5, resourceSubdirectoryFlag | 128}})
	putResourceDirectory(t, data, 96, [][2]uint32{{1, resourceSubdirectoryFlag | 160}})
	putResourceDirectory(t, data, 128, [][2]uint32{{1033, 192}})
	putResourceDirectory(t, data, 160, [][2]uint32{{1033, 208}})

	icon := testPNG(t, 16, 16)
	group := testGroupIcon(16, 16, uint32(len(icon)), 5)
	copy(data[256:], icon)
	copy(data[512:], group)
	putResourceData(t, data, 192, 0x1100, uint32(len(icon)))
	putResourceData(t, data, 208, 0x1200, uint32(len(group)))

	icons := resourceTypePayloads(data, 0, 0x1000, peResourceIcon)
	groups := resourceTypePayloads(data, 0, 0x1000, peResourceGroupIcon)
	if len(icons[5]) != 1 || len(groups[1]) != 1 {
		t.Fatalf("icon payloads = %d, group payloads = %d; want 1 each", len(icons[5]), len(groups[1]))
	}
	if err := validateGroupIcon(groups[1][0], icons); err != nil {
		t.Fatalf("valid grouped icon rejected: %v", err)
	}
	if len(resourceTypePayloads(data, 0, 0x1000, 99)) != 0 {
		t.Fatal("unexpected resource type was accepted")
	}
}

func TestValidateGroupIconRejectsMalformedOrUnlinkedResources(t *testing.T) {
	icon := testPNG(t, 16, 16)
	truncatedPNG := icon[:len(icon)/2]
	headerOnlyDIB := testDIBHeader(16, 16, 32)
	tests := map[string]struct {
		group []byte
		icons map[uint32][][]byte
	}{
		"malformed group": {group: []byte{0, 0, 1, 0, 1, 0}, icons: map[uint32][][]byte{5: {icon}}},
		"missing icon":    {group: testGroupIcon(16, 16, uint32(len(icon)), 5), icons: nil},
		"wrong size":      {group: testGroupIcon(16, 16, uint32(len(icon))+1, 5), icons: map[uint32][][]byte{5: {icon}}},
		"invalid icon":    {group: testGroupIcon(16, 16, 4, 5), icons: map[uint32][][]byte{5: {{0, 0, 0, 0}}}},
		"truncated PNG":   {group: testGroupIcon(16, 16, uint32(len(truncatedPNG)), 5), icons: map[uint32][][]byte{5: {truncatedPNG}}},
		"header-only DIB": {group: testGroupIcon(16, 16, uint32(len(headerOnlyDIB)), 5), icons: map[uint32][][]byte{5: {headerOnlyDIB}}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateGroupIcon(test.group, test.icons); err == nil {
				t.Fatal("invalid grouped icon was accepted")
			}
		})
	}
}

func TestValidateGroupIconAcceptsCompleteDIB(t *testing.T) {
	icon := testDIBHeader(16, 16, 32)
	icon = append(icon, make([]byte, 16*16*4+16*4)...)
	group := testGroupIcon(16, 16, uint32(len(icon)), 5)
	if err := validateGroupIcon(group, map[uint32][][]byte{5: {icon}}); err != nil {
		t.Fatalf("complete DIB rejected: %v", err)
	}
}

func TestResourceTypePayloadsRejectsEmptyOrOutOfBoundsData(t *testing.T) {
	for name, test := range map[string]struct {
		rva  uint32
		size uint32
	}{
		"empty":          {rva: 0x1100, size: 0},
		"before section": {rva: 0x0fff, size: 4},
		"past section":   {rva: 0x11ff, size: 4},
	} {
		t.Run(name, func(t *testing.T) {
			data := make([]byte, 512)
			putResourceDirectory(t, data, 0, [][2]uint32{{peResourceIcon, resourceSubdirectoryFlag | 64}})
			putResourceDirectory(t, data, 64, [][2]uint32{{5, resourceSubdirectoryFlag | 96}})
			putResourceDirectory(t, data, 96, [][2]uint32{{1033, 128}})
			putResourceData(t, data, 128, test.rva, test.size)
			if payloads := resourceTypePayloads(data, 0, 0x1000, peResourceIcon); len(payloads[5]) != 0 {
				t.Fatal("invalid resource data was accepted")
			}
		})
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testGroupIcon(width, height byte, size uint32, iconID uint16) []byte {
	group := make([]byte, 20)
	binary.LittleEndian.PutUint16(group[2:4], 1)
	binary.LittleEndian.PutUint16(group[4:6], 1)
	group[6] = width
	group[7] = height
	binary.LittleEndian.PutUint32(group[14:18], size)
	binary.LittleEndian.PutUint16(group[18:20], iconID)
	return group
}

func testDIBHeader(width, height int32, bitCount uint16) []byte {
	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[:4], 40)
	binary.LittleEndian.PutUint32(header[4:8], uint32(width))
	binary.LittleEndian.PutUint32(header[8:12], uint32(height*2))
	binary.LittleEndian.PutUint16(header[12:14], 1)
	binary.LittleEndian.PutUint16(header[14:16], bitCount)
	return header
}

func putResourceDirectory(t *testing.T, data []byte, offset uint32, entries [][2]uint32) {
	t.Helper()
	binary.LittleEndian.PutUint16(data[offset+14:], uint16(len(entries)))
	for index, entry := range entries {
		entryOffset := offset + 16 + uint32(index)*8
		binary.LittleEndian.PutUint32(data[entryOffset:], entry[0])
		binary.LittleEndian.PutUint32(data[entryOffset+4:], entry[1])
	}
}

func putResourceData(t *testing.T, data []byte, offset, rva, size uint32) {
	t.Helper()
	binary.LittleEndian.PutUint32(data[offset:], rva)
	binary.LittleEndian.PutUint32(data[offset+4:], size)
}
