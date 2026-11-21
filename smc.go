//go:build darwin

package main

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/IOKitLib.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdint.h>
#include <stddef.h>
#include <string.h>

typedef struct {
	uint32_t key;
	struct { uint8_t major, minor, build, reserved; uint16_t release; } vers;
	struct { uint16_t version, length; uint32_t cpuPLimit, gpuPLimit, memPLimit; } pLimitData;
	struct { uint32_t dataSize, dataType; uint8_t dataAttributes; } keyInfo;
	uint8_t result;
	uint8_t status;
	uint8_t data8;
	uint32_t data32;
	uint8_t bytes[32];
} mf_smc_key_data_t;

_Static_assert(sizeof(mf_smc_key_data_t) == 80, "SMC wire struct must be 80 bytes");
_Static_assert(offsetof(mf_smc_key_data_t, keyInfo) == 28, "SMC keyInfo offset");
_Static_assert(offsetof(mf_smc_key_data_t, result) == 40, "SMC result offset");
_Static_assert(offsetof(mf_smc_key_data_t, data8) == 42, "SMC command offset");
_Static_assert(offsetof(mf_smc_key_data_t, bytes) == 48, "SMC payload offset");

static uint32_t mf_fourcc(const char *s) {
	uint32_t v = 0;
	for (int i = 0; i < 4; i++)
		v = (v << 8) | (uint8_t)(s[i] ? s[i] : ' ');
	return v;
}

static int mf_call(io_connect_t conn, mf_smc_key_data_t *in, mf_smc_key_data_t *out) {
	size_t out_size = sizeof(mf_smc_key_data_t);
	kern_return_t kr = IOConnectCallStructMethod(conn, 2, in, sizeof(*in), out, &out_size);
	if (kr != kIOReturnSuccess)
		return (int)kr;
	if (out->result != 0)
		return 100 + (int)out->result;
	return 0;
}

static int mf_smc_open(io_connect_t *conn_out) {
	CFMutableDictionaryRef match = IOServiceMatching("AppleSMC");
	if (!match)
		return -1;

	io_iterator_t iter = 0;
	if (IOServiceGetMatchingServices(kIOMainPortDefault, match, &iter) != KERN_SUCCESS)
		return -1;

	io_service_t service = 0;
	io_service_t entry = 0;
	while ((entry = IOIteratorNext(iter))) {
		io_name_t name;
		if (IORegistryEntryGetName(entry, name) == KERN_SUCCESS &&
		    strcmp(name, "AppleSMCKeysEndpoint") == 0) {
			service = entry;
			break;
		}
		IOObjectRelease(entry);
	}
	IOObjectRelease(iter);

	if (!service)
		return -1;

	io_connect_t conn = 0;
	kern_return_t kr = IOServiceOpen(service, mach_task_self(), 0, &conn);
	IOObjectRelease(service);
	if (kr != kIOReturnSuccess || conn == 0)
		return -3;

	*conn_out = conn;
	return 0;
}

static void mf_smc_close(io_connect_t conn) {
	if (conn)
		IOServiceClose(conn);
}

static int mf_smc_read(io_connect_t conn, const char *key,
                       uint32_t *type, uint32_t *size, uint8_t *buf, uint32_t cap) {
	mf_smc_key_data_t in, out;
	memset(&in, 0, sizeof(in));
	memset(&out, 0, sizeof(out));
	in.key = mf_fourcc(key);
	in.data8 = 9;

	int err = mf_call(conn, &in, &out);
	if (err)
		return err;

	uint32_t sz = out.keyInfo.dataSize;
	if (sz == 0 || sz > cap || sz > 32)
		return -2;

	mf_smc_key_data_t in2, out2;
	memset(&in2, 0, sizeof(in2));
	memset(&out2, 0, sizeof(out2));
	in2.key = in.key;
	in2.keyInfo.dataSize = sz;
	in2.data8 = 5;

	err = mf_call(conn, &in2, &out2);
	if (err)
		return err;

	memcpy(buf, out2.bytes, sz);
	*type = out.keyInfo.dataType;
	*size = sz;
	return 0;
}

static int mf_smc_write(io_connect_t conn, const char *key, uint32_t type,
                        const uint8_t *buf, uint32_t size) {
	if (size == 0 || size > 32)
		return -2;

	mf_smc_key_data_t in, out;
	memset(&in, 0, sizeof(in));
	memset(&out, 0, sizeof(out));
	in.key = mf_fourcc(key);
	in.keyInfo.dataSize = size;
	in.keyInfo.dataType = type;
	in.data8 = 6;
	memcpy(in.bytes, buf, size);

	return mf_call(conn, &in, &out);
}
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"math"
	"unsafe"
)

// SMC error codes. Negative values -1..-4 are local, 100+n is an SMC
// result code, anything else is a raw IOKit kern_return_t.
type smcError int

func (e smcError) Error() string {
	switch int(e) {
	case -1:
		return "AppleSMCKeysEndpoint service not found"
	case -2:
		return "invalid SMC payload size"
	case -3:
		return "cannot open AppleSMC user client"
	case -4:
		return "fan does not expose the required SMC keys"
	}
	code := uint32(int32(int(e)))
	switch code {
	case 0xE00002C1:
		return "operation not permitted (run as root)"
	case 0xE00002C2:
		return "bad argument"
	case 0xE00002C7:
		return "not permitted"
	}
	if int(e) >= 100 && int(e) < 356 {
		return fmt.Sprintf("SMC result 0x%02X", int(e)-100)
	}
	return fmt.Sprintf("IOKit error 0x%08X", code)
}

type SMC struct {
	conn C.io_connect_t
}

func OpenSMC() (*SMC, error) {
	var conn C.io_connect_t
	if rc := C.mf_smc_open(&conn); rc != 0 {
		return nil, smcError(rc)
	}
	return &SMC{conn: conn}, nil
}

func (s *SMC) Close() {
	if s.conn != 0 {
		C.mf_smc_close(s.conn)
		s.conn = 0
	}
}

func (s *SMC) ReadKey(key string) (typ uint32, data []byte, err error) {
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))

	buf := make([]byte, 32)
	var ctyp, csize C.uint32_t
	rc := C.mf_smc_read(s.conn, ck, &ctyp, &csize,
		(*C.uint8_t)(unsafe.Pointer(&buf[0])), C.uint32_t(len(buf)))
	if rc != 0 {
		return 0, nil, smcError(rc)
	}
	n := int(csize)
	if n <= 0 || n > len(buf) {
		return 0, nil, smcError(-2)
	}
	return uint32(ctyp), append([]byte(nil), buf[:n]...), nil
}

func (s *SMC) ReadNumber(key string) (float64, uint32, error) {
	typ, data, err := s.ReadKey(key)
	if err != nil {
		return 0, 0, err
	}
	v, ok := decodeValue(typ, data)
	if !ok {
		return 0, typ, smcError(-2)
	}
	return v, typ, nil
}

func (s *SMC) ReadU8(key string) (uint8, error) {
	_, data, err := s.ReadKey(key)
	if err != nil {
		return 0, err
	}
	if len(data) < 1 {
		return 0, smcError(-2)
	}
	return data[0], nil
}

func (s *SMC) WriteKey(key string, typ uint32, data []byte) error {
	if len(data) == 0 || len(data) > 32 {
		return smcError(-2)
	}
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	rc := C.mf_smc_write(s.conn, ck, C.uint32_t(typ),
		(*C.uint8_t)(unsafe.Pointer(&data[0])), C.uint32_t(len(data)))
	if rc != 0 {
		return smcError(rc)
	}
	return nil
}

func (s *SMC) WriteU8(key string, value uint8) error {
	return s.WriteKey(key, fourCC("ui8 "), []byte{value})
}

func (s *SMC) WriteNumber(key string, typ uint32, value float64) error {
	data, ok := encodeValue(typ, value)
	if !ok {
		return smcError(-2)
	}
	return s.WriteKey(key, typ, data)
}

func fourCC(s string) uint32 {
	var v uint32
	for i := 0; i < 4; i++ {
		c := byte(' ')
		if i < len(s) {
			c = s[i]
		}
		v = v<<8 | uint32(c)
	}
	return v
}

func decodeValue(typ uint32, b []byte) (float64, bool) {
	switch typ {
	case fourCC("flt "):
		if len(b) < 4 {
			return 0, false
		}
		// Apple Silicon stores "flt " payloads little-endian.
		bits := binary.LittleEndian.Uint32(b)
		return float64(math.Float32frombits(bits)), true
	case fourCC("fpe2"):
		if len(b) < 2 {
			return 0, false
		}
		return float64(binary.BigEndian.Uint16(b)) / 4.0, true
	case fourCC("sp78"):
		if len(b) < 2 {
			return 0, false
		}
		return float64(int16(binary.BigEndian.Uint16(b))) / 256.0, true
	case fourCC("ui8 "):
		if len(b) < 1 {
			return 0, false
		}
		return float64(b[0]), true
	case fourCC("ui16"):
		if len(b) < 2 {
			return 0, false
		}
		return float64(binary.BigEndian.Uint16(b)), true
	case fourCC("ui32"):
		if len(b) < 4 {
			return 0, false
		}
		return float64(binary.BigEndian.Uint32(b)), true
	case fourCC("si8 "):
		if len(b) < 1 {
			return 0, false
		}
		return float64(int8(b[0])), true
	}
	return 0, false
}

func encodeValue(typ uint32, v float64) ([]byte, bool) {
	switch typ {
	case fourCC("flt "):
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, math.Float32bits(float32(v)))
		return buf, true
	case fourCC("fpe2"):
		if v < 0 {
			v = 0
		}
		return []byte{byte(uint16(v*4+0.5) >> 8), byte(uint16(v*4 + 0.5))}, true
	case fourCC("ui8 "):
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return []byte{byte(v + 0.5)}, true
	case fourCC("ui16"):
		return []byte{byte(uint16(v+0.5) >> 8), byte(uint16(v + 0.5))}, true
	case fourCC("ui32"):
		raw := uint32(v + 0.5)
		return []byte{byte(raw >> 24), byte(raw >> 16), byte(raw >> 8), byte(raw)}, true
	}
	return nil, false
}
