//go:build linux

package hardware

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const sesIOCtl = 0x2285 // SG_IO ioctl number (Linux SCSI generic driver)

const scsiReceiveDiag byte = 0x1C // RECEIVE DIAGNOSTIC RESULTS opcode

const (
	sesPageConfig   = 0x01
	sesPageStatus   = 0x02
	sesPageElemDesc = 0x07
)

func sesElementTypeName(t byte) string {
	switch t {
	case 0x00:
		return "Unspecified"
	case 0x01:
		return "Device Slot"
	case 0x02:
		return "Power Supply"
	case 0x03:
		return "Cooling"
	case 0x04:
		return "Temperature Sensor"
	case 0x05:
		return "Door"
	case 0x06:
		return "Audible Alarm"
	case 0x07:
		return "Enclosure Services Controller"
	case 0x08:
		return "SCC Controller Electronics"
	case 0x09:
		return "Nonvolatile Cache"
	case 0x0A:
		return "Invalid Operation Reason"
	case 0x0B:
		return "Uninterruptible Power Supply"
	case 0x0C:
		return "Display"
	case 0x0D:
		return "Key Pad Entry"
	case 0x0E:
		return "Enclosure"
	case 0x0F:
		return "SCSI Port/Transceiver"
	case 0x10:
		return "Language"
	case 0x11:
		return "Communication Port"
	case 0x12:
		return "Voltage Sensor"
	case 0x13:
		return "Current Sensor"
	case 0x14:
		return "SCSI Target Port"
	case 0x15:
		return "SCSI Initiator Port"
	case 0x16:
		return "Simple Subenclosure"
	case 0x17:
		return "Array Device Slot"
	case 0x18:
		return "SAS Expander"
	case 0x19:
		return "SAS Connector"
	default:
		if t >= 0x80 {
			return fmt.Sprintf("Vendor Specific (0x%02x)", t)
		}
		return fmt.Sprintf("Reserved (0x%02x)", t)
	}
}

var sesStatusNames = []string{
	"OK", "Unsupported", "Not Installed", "Critical",
	"Noncritical", "Unrecoverable", "Not Available", "No Access Allowed",
}

func sesStatusString(code byte) string {
	code &= 0x0f
	if int(code) < len(sesStatusNames) {
		return sesStatusNames[code]
	}
	return fmt.Sprintf("Unknown (0x%02x)", code)
}

// sesHdr mirrors struct sg_io_hdr from <scsi/sg.h>.
// Fixed-size integers match the C struct layout exactly on both 32- and 64-bit.
type sesHdr struct {
	interfaceID    int32
	dxferDirection int32
	cmdLen         uint8
	mxSbLen        uint8
	iovecCount     uint16
	dxferLen       uint32
	dxferp         uintptr
	cmdp           uintptr
	sbp            uintptr
	timeout        uint32
	flags          uint32
	packID         int32
	_              uintptr // usr_ptr
	status         uint8
	maskedStatus   uint8
	msgStatus      uint8
	sbLenWr        uint8
	hostStatus     uint16
	driverStatus   uint16
	resid          int32
	duration       uint32
	info           uint32
}

// receiveDiag issues RECEIVE DIAGNOSTIC RESULTS (0x1C) and fills buf with the response.
func receiveDiag(fd uintptr, pageCode byte, buf []byte) error {
	n := len(buf)
	cmd := [6]byte{scsiReceiveDiag, 0x01, pageCode, byte(n >> 8), byte(n), 0x00}
	sense := make([]byte, 32)
	hdr := sesHdr{
		interfaceID:    int32('S'),
		dxferDirection: -3, // SG_DXFER_FROM_DEV
		cmdLen:         6,
		mxSbLen:        uint8(len(sense)),
		dxferLen:       uint32(n),
		timeout:        10000, // 10 seconds
	}
	hdr.cmdp = uintptr(unsafe.Pointer(&cmd[0]))
	hdr.sbp = uintptr(unsafe.Pointer(&sense[0]))
	if n > 0 {
		hdr.dxferp = uintptr(unsafe.Pointer(&buf[0]))
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, sesIOCtl, uintptr(unsafe.Pointer(&hdr)))
	if errno != 0 {
		return fmt.Errorf("SG_IO ioctl: %w", errno)
	}
	if hdr.status != 0 {
		return fmt.Errorf("SCSI status 0x%02x", hdr.status)
	}
	return nil
}

// sesTypeHeader holds a parsed type descriptor header from SES page 0x01.
type sesTypeHeader struct {
	elementType byte
	numElements byte
	text        string
}

// parseConfigPage parses SES Configuration page (0x01) and returns the ordered
// type descriptor headers with their text labels.
func parseConfigPage(data []byte) ([]sesTypeHeader, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("config page too short (%d bytes)", len(data))
	}
	if data[0] != sesPageConfig {
		return nil, fmt.Errorf("unexpected page code 0x%02x", data[0])
	}
	nsub := int(data[1]) // number of secondary subenclosures; primary adds 1
	pageLen := int(binary.BigEndian.Uint16(data[2:4]))
	end := 4 + pageLen
	if len(data) < end {
		end = len(data)
	}

	// Walk enclosure descriptors to collect per-enclosure header counts.
	type encInfo struct{ numHdrs int }
	encs := make([]encInfo, 0, nsub+1)
	off := 4
	for i := 0; i <= nsub; i++ {
		if off+4 > end {
			break
		}
		encs = append(encs, encInfo{int(data[off+2])})
		off += 4 + int(data[off+3]) // 4 fixed bytes + ENCLOSURE DESCRIPTOR LENGTH
	}

	// Parse type descriptor headers (4 bytes each): elementType, numElements, subencID, textLen.
	type rawHdr struct{ elementType, numElements, textLen byte }
	var rawHdrs []rawHdr
	for _, enc := range encs {
		for j := 0; j < enc.numHdrs; j++ {
			if off+4 > end {
				break
			}
			rawHdrs = append(rawHdrs, rawHdr{data[off], data[off+1], data[off+3]})
			off += 4
		}
	}

	// Read the text list that follows all headers.
	headers := make([]sesTypeHeader, len(rawHdrs))
	for i, rh := range rawHdrs {
		headers[i].elementType = rh.elementType
		headers[i].numElements = rh.numElements
		tl := int(rh.textLen)
		if tl > 0 && off+tl <= end {
			headers[i].text = strings.TrimSpace(string(data[off : off+tl]))
			off += tl
		}
	}
	return headers, nil
}

// parseStatusPage parses SES Enclosure Status page (0x02) using the type headers
// from page 0x01. Returns one SESElement per individual element (overall descriptors
// are skipped). Device Slot and Temperature Sensor get type-specific detail fields.
func parseStatusPage(data []byte, headers []sesTypeHeader) []SESElement {
	elements := make([]SESElement, 0)
	if len(data) < 4 || data[0] != sesPageStatus {
		return elements
	}
	pageLen := int(binary.BigEndian.Uint16(data[2:4]))
	end := 4 + pageLen
	if len(data) < end {
		end = len(data)
	}

	off := 4
	for _, h := range headers {
		typeName := sesElementTypeName(h.elementType)
		if off+4 > end {
			break
		}
		off += 4 // skip overall status descriptor

		for idx := 0; idx < int(h.numElements); idx++ {
			if off+4 > end {
				break
			}
			d := data[off : off+4]
			name := fmt.Sprintf("%s %d", typeName, idx)

			var details string
			switch h.elementType {
			case 0x01, 0x17: // Device Slot, Array Device Slot
				// byte 3: bit7=DEVICE_OFF, bit5=FAULT_SENSED, bit1=IDENT
				var flags []string
				if d[3]&0x02 != 0 {
					flags = append(flags, "IDENT")
				}
				if d[3]&0x20 != 0 {
					flags = append(flags, "FAULT")
				}
				if d[3]&0x80 != 0 {
					flags = append(flags, "OFF")
				}
				details = strings.Join(flags, " ")
			case 0x04: // Temperature Sensor: byte 3 = temp + 20 degrees C
				details = fmt.Sprintf("%d C", int(d[3])-20)
			}

			elements = append(elements, SESElement{
				Type:       typeName,
				Descriptor: name,
				Status:     sesStatusString(d[0]),
				Details:    details,
			})
			off += 4
		}
	}
	return elements
}

// enrichFromDescPage overwrites element Descriptor fields with text from SES page
// 0x07 (Element Descriptor). Called best-effort: any parse error stops enrichment
// silently; already-enriched elements keep their names.
func enrichFromDescPage(elements []SESElement, headers []sesTypeHeader, data []byte) {
	if len(data) < 4 || data[0] != sesPageElemDesc {
		return
	}
	pageLen := int(binary.BigEndian.Uint16(data[2:4]))
	end := 4 + pageLen
	if len(data) < end {
		end = len(data)
	}

	off := 4
	ei := 0
	for _, h := range headers {
		if off+2 > end {
			return
		}
		// Skip overall element descriptor.
		overallLen := int(binary.BigEndian.Uint16(data[off : off+2]))
		off += 2 + overallLen

		for idx := 0; idx < int(h.numElements); idx++ {
			_ = idx
			if off+2 > end {
				return
			}
			dLen := int(binary.BigEndian.Uint16(data[off : off+2]))
			off += 2
			if dLen > 0 && off+dLen <= end {
				text := strings.TrimSpace(string(data[off : off+dLen]))
				if text != "" && ei < len(elements) {
					elements[ei].Descriptor = text
				}
				off += dLen
			}
			ei++
		}
	}
}

// GetSESElements reads SES element status from the given sg device by issuing
// direct SG_IO RECEIVE DIAGNOSTIC RESULTS ioctls. It replaces the sg_ses subprocess.
// Pages read: 0x01 (Configuration), 0x02 (Status), 0x07 (Element Descriptor, best-effort).
func GetSESElements(sgDev string) ([]SESElement, error) {
	if !sgDevRe.MatchString(sgDev) {
		return nil, fmt.Errorf("invalid sg device path: %s", sgDev)
	}
	f, err := os.Open(sgDev)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", sgDev, err)
	}
	defer f.Close()
	fd := f.Fd()

	configBuf := make([]byte, 4096)
	if err := receiveDiag(fd, sesPageConfig, configBuf); err != nil {
		return nil, fmt.Errorf("SES config page: %w", err)
	}
	headers, err := parseConfigPage(configBuf)
	if err != nil {
		return nil, fmt.Errorf("parse SES config page: %w", err)
	}

	statusBuf := make([]byte, 4096)
	if err := receiveDiag(fd, sesPageStatus, statusBuf); err != nil {
		return nil, fmt.Errorf("SES status page: %w", err)
	}
	elements := parseStatusPage(statusBuf, headers)

	// Page 0x07 enrichment is best-effort: descriptor names are optional.
	descBuf := make([]byte, 4096)
	if err := receiveDiag(fd, sesPageElemDesc, descBuf); err == nil {
		enrichFromDescPage(elements, headers, descBuf)
	}

	return elements, nil
}
